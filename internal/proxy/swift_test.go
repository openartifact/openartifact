// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/pkg/swift"
	"github.com/openartifact/openartifact/pkg/swift/swifttest"
)

const (
	swiftTestBase     = "https://oa.example"
	swiftTestRepo     = "registry"
	swiftTestRoot     = swiftTestBase + "/repos/swift/" + swiftTestRepo
	swiftUpstreamHost = "https://upstream.invalid"
	swiftOldDate      = "2024-01-02T03:04:05Z"
)

// fakeSwiftRegistry emulates a Swift package registry for mona.LinkedList: every
// URL it hands out is on swiftUpstreamHost, which the proxy must not pass on.
type fakeSwiftRegistry struct {
	tb testing.TB
	// versions are the releases, newest first.
	versions []string
	// published overrides the publishedAt of a version (default swiftOldDate).
	published map[string]string
	// alternates are the Swift versions with a version-specific manifest, by release.
	alternates map[string][]string
	// tamper serves archive bytes that are not the ones the release checksums.
	tamper bool
	// redirectAlternates answers every swift-version request with a redirect to
	// Package.swift, as a registry does for a version it has no manifest for.
	redirectAlternates bool

	mu      sync.Mutex
	hits    map[string]int
	accepts map[string]string
}

func newFakeSwiftRegistry(tb testing.TB) *fakeSwiftRegistry {
	tb.Helper()

	return &fakeSwiftRegistry{
		tb:         tb,
		versions:   []string{"2.0.0-rc.1", "1.1.0", "1.0.0"},
		published:  map[string]string{},
		alternates: map[string][]string{"1.0.0": {"5.4", "5.7"}},
		hits:       map[string]int{},
		accepts:    map[string]string{},
	}
}

func (g *fakeSwiftRegistry) archive(version string) []byte {
	return swifttest.Package(g.tb, "", "LinkedList", map[string]string{"VERSION": version})
}

func (g *fakeSwiftRegistry) hit(kind string, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.hits[kind]++
	g.accepts[kind] = r.Header.Get("Accept")
}

func (g *fakeSwiftRegistry) count(kind string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.hits[kind]
}

func (g *fakeSwiftRegistry) url(version string) string {
	return swiftUpstreamHost + "/mona/LinkedList/" + version
}

func (g *fakeSwiftRegistry) handler(w http.ResponseWriter, r *http.Request) {
	p, ok := swift.ParsePath(strings.TrimPrefix(r.URL.Path, "/"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	if p.Kind == swift.KindIdentifiers {
		g.hit("identifiers", r)
		g.identifiers(w, r)

		return
	}

	if swift.Key(p.Scope, p.Name) != "mona.linkedlist" || (p.Version != "" && !slices.Contains(g.versions, p.Version)) {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case swift.KindList:
		g.hit("list", r)
		g.list(w)
	case swift.KindRelease:
		g.hit("release", r)
		g.release(w, p.Version)
	case swift.KindManifest:
		g.manifest(w, r, p.Version)
	case swift.KindArchive:
		g.hit("archive", r)
		w.Header().Set("Content-Type", swift.ContentTypeZip)

		body := g.archive(p.Version)
		if g.tamper {
			body = append(body, 'x')
		}

		_, _ = w.Write(body)
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeSwiftRegistry) list(w http.ResponseWriter) {
	releases := map[string]swift.ReleaseEntry{}
	for _, v := range g.versions {
		releases[v] = swift.ReleaseEntry{URL: g.url(v)}
	}

	w.Header().Set("Content-Type", swift.MediaJSON)
	w.Header().Add("Link", swift.Link{URL: g.url(swift.Latest(g.versions)), Rel: swift.RelLatest}.String())
	_ = json.NewEncoder(w).Encode(map[string]any{"releases": releases})
}

func (g *fakeSwiftRegistry) release(w http.ResponseWriter, version string) {
	published := swiftOldDate
	if p, ok := g.published[version]; ok {
		published = p
	}

	sum := sha256.Sum256(g.archive(version))

	i := slices.Index(g.versions, version)
	h := w.Header()
	h.Add("Link", swift.Link{URL: g.url(swift.Latest(g.versions)), Rel: swift.RelLatest}.String())

	if i > 0 {
		h.Add("Link", swift.Link{URL: g.url(g.versions[i-1]), Rel: swift.RelSuccessor}.String())
	}

	if i < len(g.versions)-1 {
		h.Add("Link", swift.Link{URL: g.url(g.versions[i+1]), Rel: swift.RelPredecessor}.String())
	}

	h.Set("Content-Type", swift.MediaJSON)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "mona.LinkedList",
		"version": version,
		"resources": []map[string]string{{
			"name": "source-archive", "type": "application/zip", "checksum": hex.EncodeToString(sum[:]),
		}},
		"metadata":    map[string]string{"description": "A linked list"},
		"publishedAt": published,
	})
}

func (g *fakeSwiftRegistry) manifest(w http.ResponseWriter, r *http.Request, version string) {
	sv := r.URL.Query().Get("swift-version")
	alternates := g.alternates[version]

	if sv != "" {
		g.hit("alternate", r)

		if g.redirectAlternates || !slices.Contains(alternates, sv) {
			http.Redirect(w, r, "/mona/LinkedList/"+version+"/Package.swift", http.StatusSeeOther)
			return
		}

		w.Header().Set("Content-Type", swift.ContentTypeManifest)
		_, _ = w.Write([]byte(swifttest.Manifest(sv, "LinkedList")))

		return
	}

	g.hit("manifest", r)

	for _, a := range alternates {
		w.Header().Add("Link", swift.Link{
			URL: g.url(version) + "/Package.swift?swift-version=" + a, Rel: swift.RelAlternate,
			Params: map[string]string{"filename": swift.ManifestName(a), "swift-tools-version": a},
		}.String())
	}

	w.Header().Set("Content-Type", swift.ContentTypeManifest)
	_, _ = w.Write([]byte(swifttest.Manifest("5.9", "LinkedList")))
}

func (g *fakeSwiftRegistry) identifiers(w http.ResponseWriter, r *http.Request) {
	if swift.NormalizeURL(r.URL.Query().Get("url")) != "github.com/mona/linkedlist" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", swift.MediaJSON)
	_, _ = w.Write([]byte(`{"identifiers":["mona.LinkedList"]}`))
}

func swiftFixture(t *testing.T, g *fakeSwiftRegistry) *fixture {
	t.Helper()

	f := newFixture(t, g.handler)
	f.repo.Format = "swift"
	f.repo.Name = swiftTestRepo
	f.repo.UpstreamURL = f.upstream.URL + "/"
	f.svc.baseURL = swiftTestBase

	return f
}

// swiftDo makes a request to the proxy with the given Accept header ("" sends none).
func swiftDo(f *fixture, method, target, accept string) *httptest.ResponseRecorder {
	f.t.Helper()

	path, query, _ := strings.Cut(target, "?")
	req := httptest.NewRequestWithContext(context.Background(), method, "/?"+query, nil)

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	return w
}

func swiftGet(f *fixture, target string) *httptest.ResponseRecorder {
	f.t.Helper()

	return swiftDo(f, http.MethodGet, target, swift.MediaJSON)
}

// swiftListed is the versions the proxy's release list names, in order.
func swiftListed(t *testing.T, f *fixture) []string {
	t.Helper()

	w := swiftGet(f, "mona/LinkedList")
	if w.Code != http.StatusOK {
		return nil
	}

	var doc struct {
		Releases map[string]json.RawMessage `json:"releases"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("release list is not JSON: %v\n%s", err, w.Body.String())
	}

	list, err := swift.ParseReleaseList(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	vs := list.Versions()
	if len(vs) != len(doc.Releases) {
		t.Fatalf("list has %d releases, %d parsed", len(doc.Releases), len(vs))
	}

	return vs
}

func swiftLinkTargets(w *httptest.ResponseRecorder) map[string]string {
	targets := map[string]string{}

	for _, l := range swift.ParseLinks(w.Header().Values("Link")) {
		targets[l.Rel] = l.URL
	}

	return targets
}

func assertSwiftProblem(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()

	if w.Code != code {
		t.Fatalf("status = %d (%s), want %d", w.Code, w.Body.String(), code)
	}

	if ct := w.Header().Get("Content-Type"); ct != swift.ContentTypeProblem {
		t.Errorf("Content-Type = %q, want problem+json", ct)
	}

	var p struct {
		Detail string `json:"detail"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Detail == "" {
		t.Errorf("body %q is not a problem document with a detail (%v)", w.Body.String(), err)
	}

	if w.Header().Get("Content-Version") != "1" {
		t.Errorf("Content-Version = %q, want 1", w.Header().Get("Content-Version"))
	}
}

func TestSwiftProxy_ListIsNewestFirstWithOurURLsAndALatestLink(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	w := swiftGet(f, "mona/LinkedList")
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}

	if w.Header().Get("Content-Version") != "1" {
		t.Errorf("Content-Version = %q", w.Header().Get("Content-Version"))
	}

	if g.accepts["list"] != swift.MediaJSON {
		t.Errorf("upstream was asked with Accept %q", g.accepts["list"])
	}

	const body = `{"releases":{` +
		`"2.0.0-rc.1":{"url":"` + swiftTestRoot + `/mona/LinkedList/2.0.0-rc.1"},` +
		`"1.1.0":{"url":"` + swiftTestRoot + `/mona/LinkedList/1.1.0"},` +
		`"1.0.0":{"url":"` + swiftTestRoot + `/mona/LinkedList/1.0.0"}}}`
	if w.Body.String() != body {
		t.Errorf("list = %s\nwant %s", w.Body.String(), body)
	}

	if got := swiftLinkTargets(w)[swift.RelLatest]; got != swiftTestRoot+"/mona/LinkedList/1.1.0" {
		t.Errorf("latest-version = %q, want the newest release that is not a pre-release", got)
	}

	if strings.Contains(w.Body.String()+w.Header().Get("Link"), "upstream.invalid") {
		t.Error("an upstream URL reached the client")
	}
}

func TestSwiftProxy_SecondRequestIsServedFromTheCache(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	for _, target := range []string{
		"mona/LinkedList", "mona/LinkedList/1.0.0", "mona/LinkedList/1.0.0/Package.swift",
		"mona/LinkedList/1.0.0/Package.swift?swift-version=5.4", "mona/LinkedList/1.0.0.zip",
		"identifiers?url=https://github.com/mona/LinkedList.git",
	} {
		accept := swift.MediaJSON
		if strings.HasSuffix(target, ".zip") {
			accept = swift.MediaZip
		}

		first := swiftDo(f, http.MethodGet, target, accept)
		if first.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", target, first.Code, first.Body.String())
		}

		before := f.hits.Load()

		second := swiftDo(f, http.MethodGet, target, accept)
		if second.Code != http.StatusOK || second.Body.String() != first.Body.String() {
			t.Errorf("cached GET %s = %d, body differs", target, second.Code)
		}

		if f.hits.Load() != before {
			t.Errorf("cached GET %s reached the upstream", target)
		}
	}
}

func TestSwiftProxy_ReleaseIsTheUpstreamDocumentWithLinksToUs(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	for _, target := range []string{"mona/LinkedList/1.1.0", "mona/LinkedList/1.1.0.json", "MONA/linkedlist/1.1.0"} {
		w := swiftGet(f, target)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", target, w.Code, w.Body.String())
		}

		rel, err := swift.ParseRelease(w.Body.Bytes(), "mona.LinkedList", "1.1.0")
		if err != nil {
			t.Fatalf("GET %s: %v", target, err)
		}

		sum := sha256.Sum256(g.archive("1.1.0"))
		if rel.Checksum() != hex.EncodeToString(sum[:]) || !strings.Contains(string(rel.Metadata), "A linked list") {
			t.Errorf("GET %s did not return the upstream document: %s", target, w.Body.String())
		}

		links := swiftLinkTargets(w)
		asked := strings.Join(strings.SplitN(target, "/", 3)[:2], "/")

		for name, version := range map[string]string{
			swift.RelLatest: "1.1.0", swift.RelSuccessor: "2.0.0-rc.1", swift.RelPredecessor: "1.0.0",
		} {
			if want := swiftTestRoot + "/" + asked + "/" + version; links[name] != want {
				t.Errorf("GET %s: Link %s = %q, want %q", target, name, links[name], want)
			}
		}

		if strings.Contains(w.Header().Get("Link"), "upstream.invalid") {
			t.Errorf("GET %s: an upstream URL reached the client", target)
		}
	}

	if got := g.count("release"); got != 1 {
		t.Errorf("upstream was asked %d times for the release, want 1 (names are case-insensitive)", got)
	}

	assertSwiftProblem(t, swiftGet(f, "mona/LinkedList/9.9.9"), http.StatusNotFound)
}

func TestSwiftProxy_ManifestsAndTheirAlternates(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	w := swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0/Package.swift", swift.MediaSwift)
	if w.Code != http.StatusOK || w.Body.String() != swifttest.Manifest("5.9", "LinkedList") {
		t.Fatalf("manifest = %d %q", w.Code, w.Body.String())
	}

	if ct := w.Header().Get("Content-Type"); ct != swift.ContentTypeManifest {
		t.Errorf("Content-Type = %q", ct)
	}

	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="Package.swift"` {
		t.Errorf("Content-Disposition = %q", cd)
	}

	if g.accepts["manifest"] != swift.MediaSwift {
		t.Errorf("upstream was asked with Accept %q", g.accepts["manifest"])
	}

	var alternates []string

	for _, l := range swift.ParseLinks(w.Header().Values("Link")) {
		if l.Rel != swift.RelAlternate {
			continue
		}

		want := swiftTestRoot + "/mona/LinkedList/1.0.0/Package.swift?swift-version="
		if !strings.HasPrefix(l.URL, want) {
			t.Errorf("alternate = %q, want it on %s", l.URL, want)
		}

		sv, _ := url.Parse(l.URL)
		alternates = append(alternates, sv.Query().Get("swift-version")+"="+l.Params["filename"]+"/"+l.Params["swift-tools-version"])
	}

	if want := []string{"5.4=Package@swift-5.4.swift/5.4", "5.7=Package@swift-5.7.swift/5.7"}; !slices.Equal(alternates, want) {
		t.Errorf("alternates = %v, want %v", alternates, want)
	}

	w = swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0/Package.swift?swift-version=5.4", swift.MediaSwift)
	if w.Code != http.StatusOK || w.Body.String() != swifttest.Manifest("5.4", "LinkedList") {
		t.Fatalf("alternate manifest = %d %q", w.Code, w.Body.String())
	}

	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="Package@swift-5.4.swift"` {
		t.Errorf("alternate Content-Disposition = %q", cd)
	}

	// A Swift version without a manifest of its own is redirected to the plain one.
	w = swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0/Package.swift?swift-version=4.2", swift.MediaSwift)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != swiftTestRoot+"/mona/LinkedList/1.0.0/Package.swift" {
		t.Errorf("unknown swift-version = %d Location %q, want 303 to the plain manifest", w.Code, w.Header().Get("Location"))
	}

	if got := g.count("alternate"); got != 1 {
		t.Errorf("upstream was asked %d times for alternates, want 1 (4.2 is not one it offered)", got)
	}
}

func TestSwiftProxy_AnAlternateTheUpstreamRedirectsToThePlainManifestIsRefused(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	g.redirectAlternates = true
	f := swiftFixture(t, g)

	w := swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0/Package.swift?swift-version=5.4", swift.MediaSwift)
	assertSwiftProblem(t, w, http.StatusBadGateway)

	if f.db.entry("repo-1", "mona/linkedlist/1.0.0/Package@swift-5.4.swift") != nil {
		t.Error("the plain manifest was cached as the alternate")
	}
}

func TestSwiftProxy_ArchiveCarriesDigestAndFilenameAndIsCachedForever(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)
	zipBytes := g.archive("1.0.0")

	w := swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0.zip", swift.MediaZip)
	if w.Code != http.StatusOK || w.Body.String() != string(zipBytes) {
		t.Fatalf("archive = %d (%d bytes), want the upstream's %d", w.Code, w.Body.Len(), len(zipBytes))
	}

	sum := sha256.Sum256(zipBytes)
	if got, want := w.Header().Get("Digest"), swift.DigestHeader(hex.EncodeToString(sum[:])); got != want || want == "" {
		t.Errorf("Digest = %q, want %q", got, want)
	}

	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="LinkedList-1.0.0.zip"` {
		t.Errorf("Content-Disposition = %q", got)
	}

	if got := w.Header().Get("Content-Type"); got != swift.ContentTypeZip {
		t.Errorf("Content-Type = %q", got)
	}

	if w.Header().Get("Content-Version") != "1" {
		t.Errorf("Content-Version = %q", w.Header().Get("Content-Version"))
	}

	if g.accepts["archive"] != swift.MediaZip {
		t.Errorf("upstream was asked with Accept %q", g.accepts["archive"])
	}

	// Cached: the Digest and the name survive, the upstream is not asked again.
	again := swiftDo(f, http.MethodGet, "mona/linkedlist/1.0.0.zip", swift.MediaZip)
	if again.Code != http.StatusOK || again.Header().Get("Digest") != w.Header().Get("Digest") {
		t.Errorf("cached archive = %d Digest %q", again.Code, again.Header().Get("Digest"))
	}

	if g.count("archive") != 1 {
		t.Errorf("archive fetched %d times, want 1", g.count("archive"))
	}

	head := swiftDo(f, http.MethodHead, "mona/LinkedList/1.0.0.zip", swift.MediaZip)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Digest") != w.Header().Get("Digest") {
		t.Errorf("HEAD = %d with %d body bytes, Digest %q", head.Code, head.Body.Len(), head.Header().Get("Digest"))
	}
}

func TestSwiftProxy_ArchiveThatIsNotTheChecksummedOneIsRefusedAndNotCached(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	g.tamper = true
	f := swiftFixture(t, g)

	assertSwiftProblem(t, swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0.zip", swift.MediaZip), http.StatusBadGateway)

	if f.db.entry("repo-1", "mona/linkedlist/1.0.0.zip") != nil {
		t.Error("an archive that failed the checksum was cached")
	}

	g.tamper = false

	if w := swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0.zip", swift.MediaZip); w.Code != http.StatusOK {
		t.Errorf("genuine archive after the refusal = %d", w.Code)
	}
}

func TestSwiftProxy_Identifiers(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	w := swiftGet(f, "identifiers?url=https://github.com/mona/LinkedList.git")
	if w.Code != http.StatusOK || w.Body.String() != `{"identifiers":["mona.LinkedList"]}` {
		t.Fatalf("identifiers = %d %s", w.Code, w.Body.String())
	}

	if w.Header().Get("Content-Version") != "1" {
		t.Errorf("Content-Version = %q", w.Header().Get("Content-Version"))
	}

	assertSwiftProblem(t, swiftGet(f, "identifiers?url=https://github.com/other/Thing"), http.StatusNotFound)
	assertSwiftProblem(t, swiftGet(f, "identifiers"), http.StatusBadRequest)
	assertSwiftProblem(t, swiftGet(f, "identifiers?url="+strings.Repeat("a", swift.MaxURLLen+1)), http.StatusBadRequest)
}

func TestSwiftProxy_WritesAreRefusedWithAProblem(t *testing.T) {
	f := swiftFixture(t, newFakeSwiftRegistry(t))

	for method, target := range map[string]string{
		http.MethodPut:    "mona/LinkedList/1.0.0",
		http.MethodDelete: "mona/LinkedList/1.0.0",
		http.MethodPost:   "login",
	} {
		w := swiftDo(f, method, target, swift.MediaJSON)
		assertSwiftProblem(t, w, http.StatusMethodNotAllowed)

		if w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s Allow = %q", method, w.Header().Get("Allow"))
		}
	}

	if f.hits.Load() != 0 {
		t.Error("a refused write reached the upstream")
	}
}

func TestSwiftProxy_AcceptIsNegotiated(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	w := swiftDo(f, http.MethodGet, "mona/LinkedList", "application/vnd.swift.registry.v2+json")
	assertSwiftProblem(t, w, http.StatusUnsupportedMediaType)
	assertSwiftProblem(t, swiftDo(f, http.MethodGet, "mona/LinkedList", "application/vnd.swift.registry.vx+json"), http.StatusBadRequest)

	if f.hits.Load() != 0 {
		t.Error("a refused request reached the upstream")
	}

	for _, accept := range []string{"", "*/*", "application/json", "application/vnd.swift.registry+json"} {
		if w := swiftDo(f, http.MethodGet, "mona/LinkedList", accept); w.Code != http.StatusOK {
			t.Errorf("Accept %q = %d, want 200", accept, w.Code)
		}
	}
}

func TestSwiftProxy_UnknownPackageIsAProblemAndUpstreamErrorsAre502(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	assertSwiftProblem(t, swiftGet(f, "mona/Other"), http.StatusNotFound)
	assertSwiftProblem(t, swiftDo(f, http.MethodGet, "mona/Other/1.0.0.zip", swift.MediaZip), http.StatusNotFound)
	assertSwiftProblem(t, swiftGet(f, "mona/Other/1.0.0/Package.swift"), http.StatusNotFound)

	broken := swiftFixture(t, g)
	broken.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})

	assertSwiftProblem(t, swiftGet(broken, "mona/LinkedList"), http.StatusBadGateway)
	assertSwiftProblem(t, swiftGet(broken, "mona/LinkedList/1.0.0"), http.StatusBadGateway)
}

func TestSwiftProxy_AnEmptyOrMalformedListIsNotServed(t *testing.T) {
	for name, body := range map[string]string{
		"empty": `{"releases":{}}`, "not json": `<html>`, "no releases": `{"identifiers":[]}`,
	} {
		f := swiftFixture(t, newFakeSwiftRegistry(t))
		f.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })

		if w := swiftGet(f, "mona/LinkedList"); w.Code < http.StatusBadRequest {
			t.Errorf("%s: list = %d, want an error", name, w.Code)
		}
	}
}

func TestSwiftProxy_ReadsTheListLinksOfAnotherOpenArtifactMount(t *testing.T) {
	// An OpenArtifact upstream hands out URLs below /repos/swift/NAME: the
	// links into the package are understood wherever the registry is mounted.
	g := newFakeSwiftRegistry(t)
	f := swiftFixture(t, g)

	hosted := func(v string) string { return "http://oa.upstream.invalid/repos/swift/hosted/mona/linkedlist/" + v }
	handler := f.upstream.Config.Handler
	f.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)

		for k, vs := range rec.Header() {
			for _, v := range vs {
				w.Header().Add(k, strings.ReplaceAll(v, g.url(""), hosted("")))
			}
		}

		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})

	w := swiftGet(f, "mona/LinkedList/1.0.0")
	if w.Code != http.StatusOK {
		t.Fatalf("release = %d %s", w.Code, w.Body.String())
	}

	if got := swiftLinkTargets(w)[swift.RelSuccessor]; got != swiftTestRoot+"/mona/LinkedList/1.1.0" {
		t.Errorf("successor = %q, want the proxy's URL of 1.1.0", got)
	}
}

func TestSwiftProxy_CurationHidesVersionsAndRefusesThem(t *testing.T) {
	for _, c := range []struct {
		name  string
		mode  string
		rules []curation.Rule
		want  []string
		code  int // of the 1.1.0 release, manifest and archive
	}{
		{"denylist", curation.ModeDenylist, []curation.Rule{blockRule("mona.linkedlist", "1.1.0")}, []string{"2.0.0-rc.1", "1.0.0"}, http.StatusForbidden},
		{"case-insensitive rule", curation.ModeDenylist, []curation.Rule{blockRule("MONA.LinkedList", "1.1.0")}, []string{"2.0.0-rc.1", "1.0.0"}, http.StatusForbidden},
		{"other package", curation.ModeDenylist, []curation.Rule{blockRule("mona.other", "")}, []string{"2.0.0-rc.1", "1.1.0", "1.0.0"}, http.StatusOK},
		{"allowlist", curation.ModeAllowlist, []curation.Rule{allowRule("mona.linkedlist", "1.0.0")}, []string{"1.0.0"}, http.StatusForbidden},
		{"whole package", curation.ModeDenylist, []curation.Rule{blockRule("mona.*", "")}, nil, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := swiftFixture(t, newFakeSwiftRegistry(t))
			f.useCuration(c.mode, c.rules...)

			if got := swiftListed(t, f); !slices.Equal(got, c.want) {
				t.Errorf("list = %v, want %v", got, c.want)
			}

			for _, target := range []string{"mona/LinkedList/1.1.0", "mona/LinkedList/1.1.0/Package.swift"} {
				if w := swiftGet(f, target); w.Code != c.code {
					t.Errorf("GET %s = %d, want %d", target, w.Code, c.code)
				}
			}

			if w := swiftDo(f, http.MethodGet, "mona/LinkedList/1.1.0.zip", swift.MediaZip); w.Code != c.code {
				t.Errorf("archive = %d, want %d", w.Code, c.code)
			}
		})
	}
}

func TestSwiftProxy_LatestLinkIsTheNewestVersionCurationLeaves(t *testing.T) {
	f := swiftFixture(t, newFakeSwiftRegistry(t))
	f.useCuration(curation.ModeDenylist, blockRule("mona.linkedlist", "1.1.0"))

	w := swiftGet(f, "mona/LinkedList")
	if got := swiftLinkTargets(w)[swift.RelLatest]; got != swiftTestRoot+"/mona/LinkedList/1.0.0" {
		t.Errorf("latest-version = %q, want 1.0.0", got)
	}

	// The release's own links do not lead to the hidden version either.
	w = swiftGet(f, "mona/LinkedList/1.0.0")
	if w.Code != http.StatusOK {
		t.Fatalf("release = %d", w.Code)
	}

	if links := swiftLinkTargets(w); len(links) != 0 {
		t.Errorf("links = %v, want none: the latest release and the successor are hidden", links)
	}
}

func TestSwiftProxy_MinimumAgeHidesYoungReleases(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	g.published["1.1.0"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	g.published["2.0.0-rc.1"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	f := swiftFixture(t, g)
	f.repo.MinAgeHours = 24

	if got := swiftListed(t, f); !slices.Equal(got, []string{"1.0.0"}) {
		t.Errorf("list = %v, want only the old release", got)
	}

	if got := swiftLinkTargets(swiftGet(f, "mona/LinkedList"))[swift.RelLatest]; got != swiftTestRoot+"/mona/LinkedList/1.0.0" {
		t.Errorf("latest-version = %q, want 1.0.0", got)
	}

	for _, target := range []string{"mona/LinkedList/1.1.0", "mona/LinkedList/1.1.0/Package.swift"} {
		if w := swiftGet(f, target); w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", target, w.Code)
		}
	}

	if w := swiftDo(f, http.MethodGet, "mona/LinkedList/1.1.0.zip", swift.MediaZip); w.Code != http.StatusForbidden {
		t.Errorf("young archive = %d, want 403", w.Code)
	}

	if g.count("archive") != 0 {
		t.Error("a young archive was fetched")
	}

	if w := swiftDo(f, http.MethodGet, "mona/LinkedList/1.0.0.zip", swift.MediaZip); w.Code != http.StatusOK {
		t.Errorf("old archive = %d, want 200", w.Code)
	}

	f.useCuration(curation.ModeDenylist, allowRule("mona.linkedlist", "1.1.0"))

	if got := swiftListed(t, f); !slices.Equal(got, []string{"1.1.0", "1.0.0"}) {
		t.Errorf("list with an exemption = %v, want 1.1.0 and 1.0.0", got)
	}
}

func TestSwiftProxy_HooksReadTheCache(t *testing.T) {
	g := newFakeSwiftRegistry(t)
	g.published["1.1.0"] = "2025-05-06T07:08:09Z"
	f := swiftFixture(t, g)
	ctx := context.Background()

	if _, ok := f.svc.swiftPublishedAt(ctx, f.repo, "mona.linkedlist", "1.1.0"); ok {
		t.Error("Published knew a release nobody asked for")
	}

	if w := swiftGet(f, "mona/LinkedList"); w.Code != http.StatusOK {
		t.Fatalf("list = %d", w.Code)
	}

	if w := swiftGet(f, "mona/LinkedList/1.1.0"); w.Code != http.StatusOK {
		t.Fatalf("release = %d", w.Code)
	}

	published, ok := f.svc.swiftPublishedAt(ctx, f.repo, "mona.linkedlist", "1.1.0")
	if want, _ := time.Parse(time.RFC3339, "2025-05-06T07:08:09Z"); !ok || !published.Equal(want) {
		t.Errorf("Published = %v, %v, want %v", published, ok, want)
	}

	versions, ok := f.svc.swiftVersions(ctx, f.repo, "mona.linkedlist")
	if !ok || len(versions) != 3 {
		t.Errorf("Versions = %v, %v, want the 3 listed", versions, ok)
	}

	pkg, version, ok := swiftCoordinates("mona/LinkedList/1.1.0.zip")
	if !ok || pkg != "mona.linkedlist" || version != "1.1.0" {
		t.Errorf("Coordinates = %q %q %v", pkg, version, ok)
	}

	if _, _, ok := swiftCoordinates("mona/LinkedList"); ok {
		t.Error("a package list has no version coordinates")
	}
}
