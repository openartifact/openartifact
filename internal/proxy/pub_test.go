// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePub is an upstream pub server plus the host its archive_url values point
// at. It knows package "demo" (releases 1.0.0 and 1.1.0) and the pre-release
// 2.0.0-beta.1.
type fakePub struct {
	mu sync.Mutex

	archives  map[string]string // "<name>/<version>" -> archive bytes
	digestSrc map[string]string // bytes the listed digest is computed from (default: archives)
	published map[string]string // "<name>/<version>" -> published time
	noDigest  bool
	badDoc    bool
	docPaths  []string
	docAccept []string
	dlPaths   []string

	static *httptest.Server
}

func newFakePub(t *testing.T) *fakePub {
	t.Helper()

	g := &fakePub{
		archives: map[string]string{
			"demo/1.0.0":        "demo 1.0.0 archive",
			"demo/1.1.0":        "demo 1.1.0 archive",
			"demo/2.0.0-beta.1": "demo beta archive",
		},
		digestSrc: map[string]string{},
		published: map[string]string{},
	}

	g.static = httptest.NewServer(http.HandlerFunc(g.serveStatic))
	t.Cleanup(g.static.Close)

	return g
}

func (g *fakePub) serveStatic(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.dlPaths = append(g.dlPaths, r.URL.Path)
	g.mu.Unlock()

	// /packages/<name>/versions/<version>.tar.gz
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[0] == "packages" && parts[2] == "versions" {
		if b, ok := g.archives[parts[1]+"/"+strings.TrimSuffix(parts[3], ".tar.gz")]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
	}

	http.NotFound(w, r)
}

func (g *fakePub) entry(name, version string) map[string]any {
	key := name + "/" + version

	src, ok := g.digestSrc[key]
	if !ok {
		src = g.archives[key]
	}

	e := map[string]any{
		"version":     version,
		"archive_url": g.static.URL + "/packages/" + name + "/versions/" + version + ".tar.gz",
		"pubspec":     map[string]any{"name": name, "version": version, "environment": map[string]any{"sdk": "^3.0.0"}},
		"retracted":   false,
	}

	if !g.noDigest {
		e["archive_sha256"] = sum(src)
	}

	if t, ok := g.published[key]; ok {
		e["published"] = t
	}

	return e
}

func (g *fakePub) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.docPaths = append(g.docPaths, r.URL.Path)
	g.docAccept = append(g.docAccept, r.Header.Get("Accept"))
	bad := g.badDoc
	g.mu.Unlock()

	if r.URL.Path != "/api/packages/demo" {
		http.NotFound(w, r)
		return
	}

	if bad {
		_, _ = fmt.Fprint(w, "<html>captive portal</html>")
		return
	}

	versions := []any{g.entry("demo", "1.0.0"), g.entry("demo", "1.1.0"), g.entry("demo", "2.0.0-beta.1")}

	w.Header().Set("Content-Type", "application/vnd.pub.v2+json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": "demo", "isDiscontinued": false, "latest": g.entry("demo", "1.1.0"), "versions": versions,
	})
}

func (g *fakePub) staticPaths() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), g.dlPaths...)
}

func pubFixture(t *testing.T) (*fixture, *fakePub) {
	t.Helper()

	g := newFakePub(t)
	f := newFixture(t, g.handler)
	f.repo.Format = "pub"
	f.repo.Name = "dart"
	f.repo.UpstreamURL = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	f.svc.SetBaseURL(oaBase + "/")

	return f, g
}

const pubArchive = "api/archives/demo-1.0.0.tar.gz"

func decodePubDoc(t *testing.T, w *httptest.ResponseRecorder) (map[string]any, []string, string) {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON (%d): %s", w.Code, w.Body.String())
	}

	var versions []string

	for _, v := range doc["versions"].([]any) {
		versions = append(versions, v.(map[string]any)["version"].(string))
	}

	latest, _ := doc["latest"].(map[string]any)
	latestVersion, _ := latest["version"].(string)

	return doc, versions, latestVersion
}

func TestPubProxy_DocumentIsRelinkedAndCached(t *testing.T) {
	f, g := pubFixture(t)

	for range 2 {
		w := f.do(http.MethodGet, "api/packages/demo")
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/vnd.pub.v2+json" {
			t.Fatalf("document = %d %s: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}

		doc, versions, latest := decodePubDoc(t, w)
		if doc["name"] != "demo" || strings.Join(versions, " ") != "1.0.0 1.1.0 2.0.0-beta.1" || latest != "1.1.0" {
			t.Fatalf("document = %s", w.Body.String())
		}

		// Every archive_url points at this repository, never at the upstream's host.
		if strings.Contains(w.Body.String(), g.static.URL) {
			t.Errorf("the upstream's archive host leaked: %s", w.Body.String())
		}

		first := doc["versions"].([]any)[0].(map[string]any)
		if first["archive_url"] != oaBase+"/repos/pub/dart/api/archives/demo-1.0.0.tar.gz" ||
			first["archive_sha256"] != sum("demo 1.0.0 archive") {
			t.Errorf("entry = %v", first)
		}

		if spec, _ := first["pubspec"].(map[string]any); spec["name"] != "demo" {
			t.Errorf("pubspec = %v", first["pubspec"])
		}
	}

	if f.hits.Load() != 1 {
		t.Errorf("upstream requests = %d, want 1", f.hits.Load())
	}

	if g.docAccept[0] != "application/vnd.pub.v2+json" {
		t.Errorf("upstream saw Accept %q", g.docAccept[0])
	}

	// The version document is cut out of the same cached copy.
	w := f.do(http.MethodGet, "api/packages/demo/versions/1.1.0")
	if one := map[string]any{}; w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &one) != nil ||
		one["version"] != "1.1.0" || one["archive_url"] != oaBase+"/repos/pub/dart/api/archives/demo-1.1.0.tar.gz" {
		t.Errorf("version = %d: %s", w.Code, w.Body.String())
	}

	if w := f.do(http.MethodGet, "api/packages/demo/versions/9.9.9"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted version = %d, want 404", w.Code)
	}

	if f.hits.Load() != 1 {
		t.Errorf("version documents reached the upstream: %d requests", f.hits.Load())
	}
}

func TestPubProxy_ValidatorFollowsTheOrigin(t *testing.T) {
	f, _ := pubFixture(t)

	a := f.do(http.MethodGet, "api/packages/demo")

	f.svc.SetBaseURL("http://other.test/")

	b := f.do(http.MethodGet, "api/packages/demo")

	if a.Header().Get("ETag") == "" || a.Header().Get("ETag") == b.Header().Get("ETag") {
		t.Errorf("ETags %q and %q must differ across origins", a.Header().Get("ETag"), b.Header().Get("ETag"))
	}

	if !strings.Contains(b.Body.String(), "http://other.test/repos/pub/dart/api/archives/demo-1.0.0.tar.gz") {
		t.Errorf("links do not follow the origin: %s", b.Body.String())
	}
}

func TestPubProxy_RefusesOtherPathsAndPublishing(t *testing.T) {
	f, _ := pubFixture(t)

	for _, path := range []string{
		"api/packages/Demo",
		"api/packages/versions/new",
		"api/packages/versions/newUpload",
		"api/packages/versions/newUploadFinish",
		"api/archives/demo-1.0.0.zip",
		"api/archives/garbage",
		"api/packages/demo/advisories/x/y",
		"packages/demo",
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}

	if f.hits.Load() != 0 {
		t.Errorf("upstream requests = %d, want 0", f.hits.Load())
	}
}

func TestPubProxy_UnknownPackageAndBadDocument(t *testing.T) {
	f, g := pubFixture(t)

	if w := f.do(http.MethodGet, "api/packages/other"); w.Code != http.StatusNotFound {
		t.Errorf("unknown package = %d, want 404", w.Code)
	}

	g.badDoc = true

	if w := f.do(http.MethodGet, "api/packages/demo"); w.Code != http.StatusBadGateway {
		t.Errorf("HTML instead of a document = %d, want 502", w.Code)
	}

	if e := f.db.entry("repo-1", "api/packages/demo"); e != nil && e.BlobKey != "" {
		t.Errorf("a refused document was cached: %+v", e)
	}
}

func TestPubProxy_ArchiveIsVerifiedCachedAndServedWithUpstreamDown(t *testing.T) {
	f, g := pubFixture(t)

	if w := f.do(http.MethodGet, pubArchive); w.Code != http.StatusOK || w.Body.String() != "demo 1.0.0 archive" {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}

	if paths := g.staticPaths(); len(paths) != 1 || paths[0] != "/packages/demo/versions/1.0.0.tar.gz" {
		t.Errorf("archive host saw %v, want the release's archive_url", paths)
	}

	e := f.db.entry("repo-1", pubArchive)
	if e == nil || e.BlobKey != sum("demo 1.0.0 archive") {
		t.Fatalf("cache entry = %+v, want blob %s", e, sum("demo 1.0.0 archive"))
	}

	if ttl := e.ExpiresAt.Sub(e.CreatedAt); ttl < 24*3600*365 {
		t.Errorf("archive TTL = %v, want immutable", ttl)
	}

	// The cached archive needs neither host.
	g.static.Close()

	if w := f.do(http.MethodGet, pubArchive); w.Code != http.StatusOK || w.Body.String() != "demo 1.0.0 archive" {
		t.Errorf("cached archive = %d %q", w.Code, w.Body.String())
	}

	// One never fetched cannot be served: 502, not a hang.
	if w := f.do(http.MethodGet, "api/archives/demo-1.1.0.tar.gz"); w.Code != http.StatusBadGateway {
		t.Errorf("uncached archive with the host down = %d, want 502", w.Code)
	}
}

func TestPubProxy_DigestMismatchIsRefusedAndNotCached(t *testing.T) {
	f, g := pubFixture(t)
	g.digestSrc["demo/1.0.0"] = "some other archive"

	if w := f.do(http.MethodGet, pubArchive); w.Code != http.StatusBadGateway {
		t.Errorf("mismatching archive = %d, want 502", w.Code)
	}

	if e := f.db.entry("repo-1", pubArchive); e != nil {
		t.Errorf("mismatching archive was cached: %+v", e)
	}
}

func TestPubProxy_ReleaseWithoutDigestIsServedUnverified(t *testing.T) {
	f, g := pubFixture(t)
	g.noDigest = true

	if w := f.do(http.MethodGet, pubArchive); w.Code != http.StatusOK || w.Body.String() != "demo 1.0.0 archive" {
		t.Errorf("archive without a listed digest = %d %q", w.Code, w.Body.String())
	}
}

func TestPubProxy_UnlistedReleaseIsNotFoundAndNotFetched(t *testing.T) {
	f, g := pubFixture(t)

	if w := f.do(http.MethodGet, "api/archives/demo-9.9.9.tar.gz"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted release = %d, want 404", w.Code)
	}

	if len(g.staticPaths()) != 0 {
		t.Errorf("archive host was asked for an unlisted release: %v", g.staticPaths())
	}
}

func TestPubGate_DocumentAndArchiveOfATooNewRelease(t *testing.T) {
	f, g := pubFixture(t)
	f.repo.MinAgeHours = 24

	now := time.Now()
	g.published = map[string]string{
		"demo/1.0.0":        now.Add(-72 * time.Hour).UTC().Format(time.RFC3339),
		"demo/1.1.0":        now.Add(-time.Hour).UTC().Format(time.RFC3339),
		"demo/2.0.0-beta.1": now.Add(-96 * time.Hour).UTC().Format(time.RFC3339),
	}

	w := f.do(http.MethodGet, "api/packages/demo")

	_, versions, latest := decodePubDoc(t, w)
	if w.Code != http.StatusOK || strings.Join(versions, " ") != "1.0.0 2.0.0-beta.1" || latest != "1.0.0" {
		t.Fatalf("document = %d %s, want 1.1.0 left out and latest recomputed", w.Code, w.Body.String())
	}

	if w := f.do(http.MethodGet, "api/packages/demo/versions/1.1.0"); w.Code != http.StatusNotFound {
		t.Errorf("too new version document = %d, want 404", w.Code)
	}

	if w := f.do(http.MethodGet, "api/archives/demo-1.1.0.tar.gz"); w.Code != http.StatusForbidden {
		t.Errorf("too new archive = %d, want 403", w.Code)
	}

	if len(g.staticPaths()) != 0 {
		t.Errorf("archive host was asked for a too new archive: %v", g.staticPaths())
	}

	if e := f.db.entry("repo-1", "api/archives/demo-1.1.0.tar.gz"); e != nil {
		t.Errorf("too new archive was cached: %+v", e)
	}

	if w := f.do(http.MethodGet, pubArchive); w.Code != http.StatusOK || w.Body.String() != "demo 1.0.0 archive" {
		t.Errorf("old enough archive = %d %q", w.Code, w.Body.String())
	}
}

func TestPubGate_AllTooNewIsNotFoundAndValidatorFollowsTheGate(t *testing.T) {
	f, g := pubFixture(t)
	f.repo.MinAgeHours = 24

	young := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	g.published = map[string]string{"demo/1.0.0": young, "demo/1.1.0": young, "demo/2.0.0-beta.1": young}

	if w := f.do(http.MethodGet, "api/packages/demo"); w.Code != http.StatusNotFound {
		t.Fatalf("document with every release too new = %d, want 404", w.Code)
	}

	g.published = nil

	f.db.expire("repo-1", "api/packages/demo")

	gated := f.do(http.MethodGet, "api/packages/demo")
	if gated.Code != http.StatusOK {
		t.Fatalf("document = %d", gated.Code)
	}

	f.repo.MinAgeHours = 48

	other := f.do(http.MethodGet, "api/packages/demo")
	if gated.Header().Get("ETag") == "" || gated.Header().Get("ETag") == other.Header().Get("ETag") {
		t.Errorf("ETags %q and %q must differ across gate settings", gated.Header().Get("ETag"), other.Header().Get("ETag"))
	}
}

func TestPubGate_OffServesEveryRelease(t *testing.T) {
	f, g := pubFixture(t)
	g.published = map[string]string{"demo/1.1.0": time.Now().UTC().Format(time.RFC3339)}

	if _, versions, _ := decodePubDoc(t, f.do(http.MethodGet, "api/packages/demo")); len(versions) != 3 {
		t.Errorf("versions = %v, want all three", versions)
	}

	if w := f.do(http.MethodGet, "api/archives/demo-1.1.0.tar.gz"); w.Code != http.StatusOK {
		t.Errorf("archive = %d, want 200", w.Code)
	}
}
