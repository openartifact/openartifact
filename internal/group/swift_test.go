// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/swift"
)

const (
	swiftBase = "http://oa.test"
	swiftRoot = swiftBase + "/repos/swift/all"
)

// swiftFake is a member that answers like a swift registry: the releases it
// holds for mona.LinkedList (newest first), the Swift versions each has a
// manifest for, the identifiers it knows, and the URLs of its own host, which
// the group must not pass on.
type swiftFake struct {
	versions    []string
	alternates  []string
	identifiers []string
	fail        bool
}

func (s *swiftFake) host(m *models.Repository, version string) string {
	u := "https://" + m.Name + ".invalid/repos/swift/" + m.Name + "/mona/LinkedList"
	if version != "" {
		u += "/" + version
	}

	return u
}

func (s *swiftFake) serve(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
	parsed, ok := swift.ParsePath(p)

	switch {
	case s.fail:
		http.Error(w, "boom", http.StatusInternalServerError)
	case !ok:
		http.NotFound(w, r)
	case parsed.Kind == swift.KindIdentifiers:
		s.serveIdentifiers(w, r)
	case swift.Key(parsed.Scope, parsed.Name) != "mona.linkedlist" || len(s.versions) == 0 ||
		(parsed.Version != "" && !slices.Contains(s.versions, parsed.Version)):
		http.NotFound(w, r)
	default:
		s.serveRelease(w, r, m, parsed)
	}
}

func (s *swiftFake) serveIdentifiers(w http.ResponseWriter, r *http.Request) {
	if len(s.identifiers) == 0 || r.URL.Query().Get("url") == "" {
		http.NotFound(w, r)
		return
	}

	writeTestJSON(w, map[string][]string{"identifiers": s.identifiers})
}

func (s *swiftFake) serveRelease(w http.ResponseWriter, r *http.Request, m *models.Repository, p swift.Path) {
	h := w.Header()

	switch p.Kind {
	case swift.KindList:
		releases := map[string]swift.ReleaseEntry{}
		for _, v := range s.versions {
			releases[v] = swift.ReleaseEntry{URL: s.host(m, v)}
		}

		h.Add("Link", swift.Link{URL: s.host(m, swift.Latest(s.versions)), Rel: swift.RelLatest}.String())
		writeTestJSON(w, map[string]any{"releases": releases})
	case swift.KindRelease:
		i := slices.Index(s.versions, p.Version)
		if i > 0 {
			h.Add("Link", swift.Link{URL: s.host(m, s.versions[i-1]), Rel: swift.RelSuccessor}.String())
		}

		if i < len(s.versions)-1 {
			h.Add("Link", swift.Link{URL: s.host(m, s.versions[i+1]), Rel: swift.RelPredecessor}.String())
		}

		writeTestJSON(w, map[string]any{
			"id": "mona.LinkedList", "version": p.Version, "origin": m.Name,
			"resources": []map[string]string{{"name": "source-archive", "type": "application/zip", "checksum": strings.Repeat("ab", 32)}},
		})
	case swift.KindManifest:
		s.serveManifest(w, r, m, p)
	case swift.KindArchive:
		h.Set("Digest", "sha-256="+m.Name)
		_, _ = w.Write([]byte("zip-" + m.Name + "-" + p.Version))
	case swift.KindIdentifiers, swift.KindLogin:
		http.NotFound(w, r)
	}
}

func (s *swiftFake) serveManifest(w http.ResponseWriter, r *http.Request, m *models.Repository, p swift.Path) {
	h := w.Header()
	h.Set("Content-Type", swift.ContentTypeManifest)

	if sv := r.URL.Query().Get("swift-version"); sv != "" {
		if !slices.Contains(s.alternates, sv) {
			h.Set("Location", s.host(m, p.Version)+"/Package.swift")
			w.WriteHeader(http.StatusSeeOther)

			return
		}

		h.Set("Content-Disposition", `attachment; filename="`+swift.ManifestName(sv)+`"`)
		_, _ = w.Write([]byte("// " + m.Name + " " + sv))

		return
	}

	for _, a := range s.alternates {
		h.Add("Link", swift.Link{
			URL: s.host(m, p.Version) + "/Package.swift?swift-version=" + a, Rel: swift.RelAlternate,
			Params: map[string]string{"filename": swift.ManifestName(a), "swift-tools-version": a},
		}.String())
	}

	h.Set("Content-Disposition", `attachment; filename="Package.swift"`)
	_, _ = w.Write([]byte("// " + m.Name))
}

type swiftGroupFixture struct {
	f     *fixture
	fakes map[string]*swiftFake
}

func newSwiftGroupFixture(t *testing.T, merge bool, members ...*models.Repository) *swiftGroupFixture {
	t.Helper()

	f := newFixture(t, "swift", members...)
	f.grp.MergeProxyVersions = merge
	f.h.SetBaseURL(swiftBase + "/")

	return &swiftGroupFixture{f: f, fakes: map[string]*swiftFake{}}
}

func (g *swiftGroupFixture) backend() Backend {
	return Backend{
		Authorize: func(_ *http.Request, m *models.Repository, _ string) bool { return !g.f.denied[m.Name] },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
			g.f.asked = append(g.f.asked, m.Name)

			fake, ok := g.fakes[m.Name]
			if !ok {
				http.NotFound(w, r)
				return
			}

			fake.serve(w, r, m, p)
		},
	}
}

// do makes a request to the group; target may carry a query.
func (g *swiftGroupFixture) do(method, target, accept string) *httptest.ResponseRecorder {
	path, query, _ := strings.Cut(target, "?")
	req := httptest.NewRequestWithContext(context.Background(), method, "/?"+query, nil)

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	w := httptest.NewRecorder()
	g.f.h.Serve(w, req, g.f.grp, path, g.backend())

	return w
}

func (g *swiftGroupFixture) get(target string) *httptest.ResponseRecorder {
	return g.do(http.MethodGet, target, swift.MediaJSON)
}

func (g *swiftGroupFixture) versions(t *testing.T) []string {
	t.Helper()

	w := g.get("mona/LinkedList")
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}

	list, err := swift.ParseReleaseList(w.Body.Bytes())
	if err != nil {
		t.Fatalf("list %s: %v", w.Body.String(), err)
	}

	return list.Versions()
}

func swiftMembers() []*models.Repository {
	return []*models.Repository{member("hosted", "swift", "hosted"), member("mirror", "swift", "proxy")}
}

func swiftLinks(w *httptest.ResponseRecorder) map[string]string {
	out := map[string]string{}

	for _, l := range swift.ParseLinks(w.Header().Values("Link")) {
		out[l.Rel] = l.URL
	}

	return out
}

func assertSwiftGroupProblem(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()

	if w.Code != code {
		t.Fatalf("status = %d (%s), want %d", w.Code, w.Body.String(), code)
	}

	var p struct {
		Detail string `json:"detail"`
	}

	if w.Header().Get("Content-Type") != swift.ContentTypeProblem || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Detail == "" {
		t.Errorf("answer %q (%s) is not a problem document", w.Body.String(), w.Header().Get("Content-Type"))
	}

	if w.Header().Get("Content-Version") != "1" {
		t.Errorf("Content-Version = %q, want 1", w.Header().Get("Content-Version"))
	}
}

func TestSwiftGroup_ListIsTheUnionNewestFirstWithGroupURLsAndLatestLink(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.1.0", "1.0.0"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"3.0.0-beta.1", "2.0.0", "1.1.0"}}

	w := g.get("mona/LinkedList")
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}

	if w.Header().Get("Content-Version") != "1" {
		t.Errorf("Content-Version = %q", w.Header().Get("Content-Version"))
	}

	want := `{"releases":{` +
		`"3.0.0-beta.1":{"url":"` + swiftRoot + `/mona/LinkedList/3.0.0-beta.1"},` +
		`"2.0.0":{"url":"` + swiftRoot + `/mona/LinkedList/2.0.0"},` +
		`"1.1.0":{"url":"` + swiftRoot + `/mona/LinkedList/1.1.0"},` +
		`"1.0.0":{"url":"` + swiftRoot + `/mona/LinkedList/1.0.0"}}}`
	if w.Body.String() != want {
		t.Errorf("list = %s\nwant %s", w.Body.String(), want)
	}

	if got := swiftLinks(w)[swift.RelLatest]; got != swiftRoot+"/mona/LinkedList/2.0.0" {
		t.Errorf("latest-version = %q, want 2.0.0 (the pre-release is not the latest)", got)
	}

	if strings.Contains(w.Body.String()+w.Header().Get("Link"), "invalid") {
		t.Error("a member's URL reached the client")
	}

	if w.Header().Get("ETag") == "" {
		t.Error("a complete merge has no validator")
	}
}

func TestSwiftGroup_ListIsRevalidatable(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}

	etag := g.get("mona/LinkedList").Header().Get("ETag")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("Accept", swift.MediaJSON)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	g.f.h.Serve(w, req, g.f.grp, "mona/LinkedList", g.backend())

	if w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}
}

func TestSwiftGroup_ReleaseAndManifestComeFromTheFirstMemberWithGroupLinks(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.1.0", "1.0.0"}, alternates: []string{"5.4"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"2.0.0", "1.1.0"}, alternates: []string{"5.7"}}

	w := g.get("mona/LinkedList/1.1.0")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"origin":"hosted"`) {
		t.Fatalf("release = %d %s, want the hosted member's", w.Code, w.Body.String())
	}

	links := swiftLinks(w)
	for rel, version := range map[string]string{swift.RelPredecessor: "1.0.0"} {
		if want := swiftRoot + "/mona/LinkedList/" + version; links[rel] != want {
			t.Errorf("Link %s = %q, want %q", rel, links[rel], want)
		}
	}

	if strings.Contains(w.Header().Get("Link"), "invalid") {
		t.Errorf("a member's URL reached the client: %s", w.Header().Get("Link"))
	}

	if w := g.get("mona/LinkedList/2.0.0"); !strings.Contains(w.Body.String(), `"origin":"mirror"`) {
		t.Errorf("a release only the mirror has = %d %s", w.Code, w.Body.String())
	}

	m := g.do(http.MethodGet, "mona/LinkedList/1.1.0/Package.swift", swift.MediaSwift)
	if m.Code != http.StatusOK || m.Body.String() != "// hosted" {
		t.Fatalf("manifest = %d %q", m.Code, m.Body.String())
	}

	if m.Header().Get("Content-Type") != swift.ContentTypeManifest ||
		m.Header().Get("Content-Disposition") != `attachment; filename="Package.swift"` {
		t.Errorf("manifest headers = %v", m.Header())
	}

	alt := swift.ParseLinks(m.Header().Values("Link"))
	if len(alt) != 1 || alt[0].Rel != swift.RelAlternate ||
		alt[0].URL != swiftRoot+"/mona/LinkedList/1.1.0/Package.swift?swift-version=5.4" ||
		alt[0].Params["filename"] != "Package@swift-5.4.swift" || alt[0].Params["swift-tools-version"] != "5.4" {
		t.Errorf("alternate links = %+v, want the hosted member's on the group", alt)
	}

	a := g.do(http.MethodGet, "mona/LinkedList/1.1.0/Package.swift?swift-version=5.4", swift.MediaSwift)
	if a.Code != http.StatusOK || a.Body.String() != "// hosted 5.4" ||
		a.Header().Get("Content-Disposition") != `attachment; filename="Package@swift-5.4.swift"` {
		t.Errorf("alternate manifest = %d %q %q", a.Code, a.Body.String(), a.Header().Get("Content-Disposition"))
	}
}

func TestSwiftGroup_UnknownSwiftVersionRedirectsToThePlainManifestOfTheGroup(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"1.0.0"}, alternates: []string{"4.2"}}

	w := g.do(http.MethodGet, "mona/LinkedList/1.0.0/Package.swift?swift-version=4.2", swift.MediaSwift)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != swiftRoot+"/mona/LinkedList/1.0.0/Package.swift" {
		t.Errorf("= %d Location %q, want 303 to the group's plain manifest (the first member with the release decides)",
			w.Code, w.Header().Get("Location"))
	}
}

func TestSwiftGroup_ArchiveIsServedByTheFirstMemberThatHasIt(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"2.0.0", "1.0.0"}}

	for target, want := range map[string]string{
		"mona/LinkedList/1.0.0.zip": "zip-hosted-1.0.0",
		"mona/LinkedList/2.0.0.zip": "zip-mirror-2.0.0",
	} {
		w := g.do(http.MethodGet, target, swift.MediaZip)
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s = %d %q, want %q", target, w.Code, w.Body.String(), want)
		}

		if !strings.HasPrefix(w.Header().Get("Digest"), "sha-256=") || w.Header().Get("Content-Version") != "1" {
			t.Errorf("%s headers = %v", target, w.Header())
		}
	}

	assertSwiftGroupProblem(t, g.do(http.MethodGet, "mona/LinkedList/9.9.9.zip", swift.MediaZip), http.StatusNotFound)
}

func TestSwiftGroup_HostedWinsHidesTheProxysVersionsAndFiles(t *testing.T) {
	g := newSwiftGroupFixture(t, false, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"9.9.9", "1.0.0"}}

	if got := g.versions(t); !slices.Equal(got, []string{"1.0.0"}) {
		t.Errorf("versions = %v, want the hosted 1.0.0 only", got)
	}

	if w := g.get("mona/LinkedList/9.9.9"); w.Code != http.StatusNotFound {
		t.Errorf("proxy-only release of a hosted package = %d, want 404", w.Code)
	}

	if w := g.do(http.MethodGet, "mona/LinkedList/9.9.9.zip", swift.MediaZip); w.Code != http.StatusNotFound {
		t.Errorf("proxy-only archive of a hosted package = %d, want 404", w.Code)
	}

	if w := g.do(http.MethodGet, "mona/LinkedList/1.0.0.zip", swift.MediaZip); w.Body.String() != "zip-hosted-1.0.0" {
		t.Errorf("archive = %q, want the hosted member's", w.Body.String())
	}

	if slices.Contains(g.f.asked, "mirror") {
		t.Errorf("the proxy was asked for a package a hosted member holds: %v", g.f.asked)
	}

	// A package no hosted member holds is the proxy's.
	g.fakes["hosted"].versions = nil

	if got := g.versions(t); !slices.Equal(got, []string{"9.9.9", "1.0.0"}) {
		t.Errorf("versions of a proxy-only package = %v", got)
	}
}

func TestSwiftGroup_MergeProxyVersionsDisablesHostedWins(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"9.9.9", "1.0.0"}}

	if got := g.versions(t); !slices.Equal(got, []string{"9.9.9", "1.0.0"}) {
		t.Errorf("versions = %v, want the union", got)
	}

	if w := g.do(http.MethodGet, "mona/LinkedList/9.9.9.zip", swift.MediaZip); w.Body.String() != "zip-mirror-9.9.9" {
		t.Errorf("proxy archive = %q, want it served", w.Body.String())
	}
}

func TestSwiftGroup_UnreadableHostedMemberCountsAsAbsent(t *testing.T) {
	g := newSwiftGroupFixture(t, false, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"9.9.9"}}
	g.f.denied["hosted"] = true

	if got := g.versions(t); !slices.Equal(got, []string{"9.9.9"}) {
		t.Errorf("versions = %v, want the proxy's", got)
	}
}

func TestSwiftGroup_FailingMembers(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}, fail: true}
	g.fakes["mirror"] = &swiftFake{versions: []string{"2.0.0", "1.0.0"}}

	// The list of the member that works is served, but never cached.
	w := g.get("mona/LinkedList")
	if w.Code != http.StatusOK || w.Header().Get("ETag") != "" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("partial list = %d ETag %q Cache-Control %q", w.Code, w.Header().Get("ETag"), w.Header().Get("Cache-Control"))
	}

	// Nothing to serve and a member failed: 502, not a cacheable 404.
	g.fakes["mirror"].versions = nil

	for target, accept := range map[string]string{
		"mona/LinkedList": swift.MediaJSON, "mona/LinkedList/1.0.0": swift.MediaJSON,
		"mona/LinkedList/1.0.0/Package.swift": swift.MediaSwift, "mona/LinkedList/1.0.0.zip": swift.MediaZip,
		"identifiers?url=https://github.com/mona/LinkedList": swift.MediaJSON,
	} {
		assertSwiftGroupProblem(t, g.do(http.MethodGet, target, accept), http.StatusBadGateway)
	}
}

func TestSwiftGroup_NothingHasIt(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}
	g.fakes["mirror"] = &swiftFake{versions: []string{"1.0.0"}}

	for target, accept := range map[string]string{
		"mona/Other": swift.MediaJSON, "mona/LinkedList/9.9.9": swift.MediaJSON,
		"mona/LinkedList/9.9.9/Package.swift": swift.MediaSwift, "mona/Other/1.0.0.zip": swift.MediaZip,
		"identifiers?url=https://github.com/mona/LinkedList": swift.MediaJSON,
	} {
		assertSwiftGroupProblem(t, g.do(http.MethodGet, target, accept), http.StatusNotFound)
	}
}

func TestSwiftGroup_IdentifiersAreTheSortedUnionOfTheMembers(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{identifiers: []string{"mona.LinkedList", "acme.Lists"}}
	g.fakes["mirror"] = &swiftFake{identifiers: []string{"mona.LinkedList", "zed.Chain"}}

	w := g.get("identifiers?url=https://github.com/mona/LinkedList")
	if w.Code != http.StatusOK || w.Body.String() != `{"identifiers":["acme.Lists","mona.LinkedList","zed.Chain"]}` {
		t.Errorf("identifiers = %d %s", w.Code, w.Body.String())
	}

	assertSwiftGroupProblem(t, g.get("identifiers"), http.StatusBadRequest)
}

func TestSwiftGroup_WritesAreRefusedWithAProblem(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)

	for method, target := range map[string]string{
		http.MethodPut:    "mona/LinkedList/1.0.0",
		http.MethodDelete: "mona/LinkedList/1.0.0",
		http.MethodPost:   "login",
	} {
		w := g.do(method, target, swift.MediaJSON)
		assertSwiftGroupProblem(t, w, http.StatusMethodNotAllowed)

		if w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s Allow = %q", method, w.Header().Get("Allow"))
		}
	}

	if len(g.f.asked) != 0 {
		t.Errorf("a refused write reached the members: %v", g.f.asked)
	}
}

func TestSwiftGroup_AcceptIsNegotiated(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)
	g.fakes["hosted"] = &swiftFake{versions: []string{"1.0.0"}}

	w := g.do(http.MethodGet, "mona/LinkedList", "application/vnd.swift.registry.v2+json")
	assertSwiftGroupProblem(t, w, http.StatusUnsupportedMediaType)
	assertSwiftGroupProblem(t, g.do(http.MethodGet, "mona/LinkedList", "application/vnd.swift.registry.vx+json"), http.StatusBadRequest)

	if w := g.do(http.MethodGet, "mona/LinkedList", ""); w.Code != http.StatusOK {
		t.Errorf("no Accept = %d, want 200", w.Code)
	}
}

func TestSwiftGroup_LoginAndStrangePathsAnswerProblems(t *testing.T) {
	g := newSwiftGroupFixture(t, true, swiftMembers()...)

	assertSwiftGroupProblem(t, g.get("a/b/c/d/e/f"), http.StatusNotFound)
	assertSwiftGroupProblem(t, g.get("login"), http.StatusMethodNotAllowed)
}
