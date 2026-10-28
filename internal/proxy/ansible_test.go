// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

const (
	ansibleBase      = "https://oa.example"
	ansibleCanonical = "/api/v3/plugin/ansible/content/published/collections/"
	ansibleTarball   = "demo tarball bytes"
)

// fakeGalaxy emulates galaxy.ansible.com: the short /api/v3/collections/ form
// redirects to the canonical plugin paths, version lists are paginated with
// limit/offset (at most pageMax per page) and tarballs are redirected to a
// content host.
type fakeGalaxy struct {
	mu       sync.Mutex
	versions []string
	tarballs map[string]string // file name -> bytes served
	pageMax  int
	// contentHost, when set, is the origin of download_url and the signed URLs.
	contentHost string
	// downloadAuth records the Authorization header the tarball request carried.
	downloadAuth atomic.Value
	tarballHits  atomic.Int32
}

func newFakeGalaxy() *fakeGalaxy {
	return &fakeGalaxy{
		versions: []string{"1.0.0", "1.1.0", "2.0.0-rc.1"},
		tarballs: map[string]string{
			"ns-demo-1.0.0.tar.gz":      ansibleTarball,
			"ns-demo-1.1.0.tar.gz":      ansibleTarball + " 1.1",
			"ns-demo-2.0.0-rc.1.tar.gz": ansibleTarball + " rc",
		},
		pageMax: 2,
	}
}

func (g *fakeGalaxy) handler(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	self := "http://" + r.Host
	p := r.URL.Path

	switch {
	case strings.HasPrefix(p, "/api/v3/collections/"):
		target := ansibleCanonical + "index/" + strings.TrimPrefix(p, "/api/v3/collections/")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}

		http.Redirect(w, r, target, http.StatusFound)
	case strings.HasPrefix(p, ansibleCanonical+"index/ns/demo/versions/") && strings.HasSuffix(p, "/versions/"):
		g.writeList(w, r, self)
	case strings.HasPrefix(p, ansibleCanonical+"index/ns/demo/versions/"):
		g.writeVersion(w, strings.TrimSuffix(strings.TrimPrefix(p, ansibleCanonical+"index/ns/demo/versions/"), "/"), self)
	case strings.HasPrefix(p, ansibleCanonical+"artifacts/"):
		g.downloadAuth.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, "/signed/"+strings.TrimPrefix(p, ansibleCanonical+"artifacts/"), http.StatusFound)
	case strings.HasPrefix(p, "/signed/"):
		g.tarballHits.Add(1)

		if body, ok := g.tarballs[strings.TrimPrefix(p, "/signed/")]; ok {
			_, _ = w.Write([]byte(body))
			return
		}

		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeGalaxy) writeList(w http.ResponseWriter, r *http.Request, self string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit = min(max(limit, 1), g.pageMax)

	data := []map[string]any{}

	for i := offset; i < min(offset+limit, len(g.versions)); i++ {
		v := g.versions[i]
		data = append(data, map[string]any{
			"version": v, "href": self + ansibleCanonical + "index/ns/demo/versions/" + v + "/",
			"created_at":       "2024-01-0" + strconv.Itoa(i+1) + "T00:00:00.000000Z",
			"updated_at":       "2024-01-0" + strconv.Itoa(i+1) + "T00:00:00.000000Z",
			"requires_ansible": ">=2.14", "marks": []string{},
		})
	}

	if len(g.versions) == 0 {
		http.NotFound(w, r)
		return
	}

	body, _ := json.Marshal(map[string]any{
		"meta": map[string]int{"count": len(g.versions)}, "links": map[string]any{"next": nil}, "data": data,
	})

	_, _ = w.Write(body)
}

func (g *fakeGalaxy) writeVersion(w http.ResponseWriter, v, self string) {
	file := "ns-demo-" + v + ".tar.gz"

	body, ok := g.tarballs[file]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	host := g.contentHost
	if host == "" {
		host = self
	}

	doc, _ := json.Marshal(map[string]any{
		"version": v, "href": self + ansibleCanonical + "index/ns/demo/versions/" + v + "/",
		"created_at": "2024-01-01T00:00:00.000000Z", "updated_at": "2024-01-01T00:00:00.000000Z",
		"marks":        []string{},
		"artifact":     map[string]any{"filename": file, "sha256": sum(ansibleTarballFor(v)), "size": len(body)},
		"collection":   map[string]string{"name": "demo", "href": self + ansibleCanonical + "index/ns/demo/"},
		"download_url": host + ansibleCanonical + "artifacts/" + file,
		"name":         "demo", "namespace": map[string]string{"name": "ns"},
		"metadata": map[string]any{"dependencies": map[string]string{"other.thing": ">=1.0.0"}}, "signatures": []any{},
	})

	_, _ = w.Write(doc)
}

// ansibleTarballFor is the content the version's document vouches for.
func ansibleTarballFor(v string) string {
	switch v {
	case "1.1.0":
		return ansibleTarball + " 1.1"
	case "2.0.0-rc.1":
		return ansibleTarball + " rc"
	}

	return ansibleTarball
}

func ansibleFixture(t *testing.T, g *fakeGalaxy) *fixture {
	t.Helper()

	f := newFixture(t, g.handler)
	f.repo.Format = "ansible"
	f.repo.Name = "galaxy"
	f.repo.UpstreamURL = f.upstream.URL + "/"
	f.svc.baseURL = ansibleBase

	return f
}

func ansibleGet(t *testing.T, f *fixture, path string, into any) *httptest.ResponseRecorder {
	t.Helper()

	path, query, _ := strings.Cut(path, "?")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?"+query, nil)
	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	if into != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
			t.Fatalf("GET %s: not JSON: %v\n%s", path, err, w.Body.String())
		}
	}

	return w
}

func TestAnsibleProxy_VersionsAreFetchedWholeCachedAndPaginatedWithOurLinks(t *testing.T) {
	g := newFakeGalaxy()
	f := ansibleFixture(t, g)

	var page struct {
		Meta  map[string]int `json:"meta"`
		Links map[string]any `json:"links"`
		Data  []struct {
			Version string `json:"version"`
			Href    string `json:"href"`
		} `json:"data"`
	}

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/?limit=2", &page); w.Code != http.StatusOK {
		t.Fatalf("versions = %d %s", w.Code, w.Body.String())
	}

	if page.Meta["count"] != 3 || len(page.Data) != 2 || page.Data[0].Version != "2.0.0-rc.1" || page.Data[1].Version != "1.1.0" {
		t.Fatalf("page 1 = %+v, want newest first, 2 of 3", page)
	}

	wantHref := ansibleBase + "/repos/galaxy/api/v3/collections/ns/demo/versions/2.0.0-rc.1/"
	if page.Data[0].Href != wantHref {
		t.Errorf("href = %q, want %q", page.Data[0].Href, wantHref)
	}

	next, _ := page.Links["next"].(string)
	if !strings.HasPrefix(next, ansibleBase+"/repos/galaxy/api/v3/collections/ns/demo/versions/") {
		t.Errorf("next = %q, want a link of this repository", next)
	}

	hits := f.hits.Load()

	// The client's second page comes from the cached combined list.
	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/?limit=2&offset=2", &page); w.Code != http.StatusOK ||
		len(page.Data) != 1 || page.Data[0].Version != "1.0.0" || page.Links["next"] != nil {
		t.Fatalf("page 2 = %d %+v", w.Code, page)
	}

	if f.hits.Load() != hits {
		t.Errorf("second page reached the upstream")
	}

	if f.db.entry("repo-1", "api/v3/collections/ns/demo/versions/") == nil {
		t.Error("combined version list is not cached under its repository-relative key")
	}
}

func TestAnsibleProxy_CollectionDocumentIsGeneratedFromTheVersionList(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())

	var doc pkgansible.CollectionDoc

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/", &doc); w.Code != http.StatusOK {
		t.Fatalf("collection = %d %s", w.Code, w.Body.String())
	}

	if doc.Namespace != "ns" || doc.Name != "demo" || doc.HighestVersion == nil || doc.HighestVersion.Version != "1.1.0" {
		t.Errorf("collection = %+v, want highest release 1.1.0", doc)
	}

	if doc.Href != ansibleBase+"/repos/galaxy/api/v3/collections/ns/demo/" || strings.Contains(w2s(doc), f.upstream.URL) {
		t.Errorf("collection links = %+v", doc)
	}
}

func w2s(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAnsibleProxy_VersionDocumentIsRelinkedAndCached(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())

	var doc pkgansible.VersionDoc

	if w := ansibleGet(t, f, "api/v3/collections/ns/demo/versions/1.0.0/", &doc); w.Code != http.StatusOK {
		t.Fatalf("version = %d %s", w.Code, w.Body.String())
	}

	if doc.DownloadURL != ansibleBase+"/repos/galaxy/api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz" {
		t.Errorf("download_url = %q", doc.DownloadURL)
	}

	if doc.Artifact.SHA256 != sum(ansibleTarball) || doc.Dependencies()["other.thing"] != ">=1.0.0" {
		t.Errorf("artifact digest or dependencies lost: %+v", doc)
	}

	if strings.Contains(w2s(doc), f.upstream.URL) {
		t.Errorf("document leaks the upstream: %s", w2s(doc))
	}

	hits := f.hits.Load()

	if w := f.do(http.MethodGet, "api/v3/collections/ns/demo/versions/1.0.0/"); w.Code != http.StatusOK || f.hits.Load() != hits {
		t.Errorf("second read = %d with %d new upstream requests, want a cache hit", w.Code, f.hits.Load()-hits)
	}
}

func TestAnsibleProxy_TarballIsVerifiedCachedAndServedWithTheUpstreamDown(t *testing.T) {
	g := newFakeGalaxy()
	f := ansibleFixture(t, g)
	path := "api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz"

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != ansibleTarball {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}

	if e := f.db.entry("repo-1", path); e == nil || e.BlobKey != sum(ansibleTarball) {
		t.Fatalf("tarball not cached under its repository-relative key: %+v", e)
	}

	f.repo.UpstreamURL = "http://127.0.0.1:1/"

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != ansibleTarball {
		t.Errorf("cached download = %d %q", w.Code, w.Body.String())
	}

	if g.tarballHits.Load() != 1 {
		t.Errorf("tarball fetched %d times, want 1", g.tarballHits.Load())
	}
}

func TestAnsibleProxy_TarballWithWrongDigestIsRefusedAndNotCached(t *testing.T) {
	g := newFakeGalaxy()
	g.tarballs["ns-demo-1.0.0.tar.gz"] = "tampered"
	f := ansibleFixture(t, g)
	path := "api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz"

	for range 2 {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Fatalf("mismatching tarball = %d %q, want 502", w.Code, w.Body.String())
		}
	}

	if f.db.entry("repo-1", path) != nil {
		t.Error("a tarball that does not match its digest was cached")
	}

	g.tarballs["ns-demo-1.0.0.tar.gz"] = ansibleTarball

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != ansibleTarball {
		t.Errorf("after the upstream is fixed = %d %q", w.Code, w.Body.String())
	}
}

func TestAnsibleProxy_TarballOnAnotherHostGetsNoCredentials(t *testing.T) {
	g := newFakeGalaxy()
	content := newFakeGalaxy()

	contentSrv := httptest.NewServer(http.HandlerFunc(content.handler))
	t.Cleanup(contentSrv.Close)

	// Bind the same file set to the content host: the document points there.
	content.tarballs = g.tarballs
	g.contentHost = contentSrv.URL

	f := ansibleFixture(t, g)
	f.repo.UpstreamUsername = "robot"

	w := f.do(http.MethodGet, "api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz")
	if w.Code != http.StatusOK || w.Body.String() != ansibleTarball {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}

	if auth, _ := content.downloadAuth.Load().(string); auth != "" {
		t.Errorf("content host received credentials: %q", auth)
	}

	if g.tarballHits.Load() != 0 || content.tarballHits.Load() != 1 {
		t.Errorf("tarball hits upstream=%d content=%d, want 0 and 1", g.tarballHits.Load(), content.tarballHits.Load())
	}
}

func TestAnsibleProxy_NotFoundAndUnservedPaths(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())

	for _, path := range []string{
		"api/v3/collections/",                               // the index cannot be enumerated
		"api/v3/collections/ns/unknown/",                    // unknown collection
		"api/v3/collections/ns/unknown/versions/",           // unknown collection
		"api/v3/collections/ns/demo/versions/9.9.9/",        // unlisted version
		"api/v3/artifacts/collections/ns-demo-9.9.9.tar.gz", // unlisted version
		"api/v3/collections/NS/demo/",                       // not a valid name
		"api/v3/collections/ns/demo/versions/1.0/",          // not a valid version
		"api/v3/artifacts/collections/ns-demo.tar.gz",       // not a file name
		"api/v2/collections/",                               // another API
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}
}

func TestAnsibleProxy_DiscoveryDocumentsAreGeneratedWithoutUpstream(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())

	var root struct {
		Versions map[string]string `json:"available_versions"`
	}

	if w := ansibleGet(t, f, "api/", &root); w.Code != http.StatusOK || root.Versions["v3"] != "v3/" {
		t.Errorf("api/ = %d %+v", w.Code, root)
	}

	var v3 map[string]string

	if w := ansibleGet(t, f, "api/v3/", &v3); w.Code != http.StatusOK ||
		v3["collections"] != ansibleBase+"/repos/galaxy/api/v3/collections/" ||
		v3["artifacts"] != ansibleBase+"/repos/galaxy/api/v3/artifacts/collections/" {
		t.Errorf("api/v3/ = %d %v", w.Code, v3)
	}

	if f.hits.Load() != 0 {
		t.Errorf("discovery reached the upstream %d times", f.hits.Load())
	}
}

func TestAnsibleProxy_BadPaginationIsRefused(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())

	for _, q := range []string{"limit=0", "limit=x", "offset=-1"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?"+q, nil)
		w := httptest.NewRecorder()
		f.svc.Serve(w, req, f.repo, "api/v3/collections/ns/demo/versions/")

		if w.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", q, w.Code)
		}
	}
}

func TestAnsibleProxy_QuarantinedTarballIsRefused(t *testing.T) {
	f := ansibleFixture(t, newFakeGalaxy())
	path := "api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz"

	f.svc.db = &quarantineDB{memDB: f.db, blocked: path}

	if w := f.do(http.MethodGet, path); w.Code != http.StatusForbidden {
		t.Errorf("quarantined download = %d, want 403", w.Code)
	}

	if w := f.do(http.MethodGet, "api/v3/artifacts/collections/ns-demo-1.1.0.tar.gz"); w.Code != http.StatusOK {
		t.Errorf("other version = %d, want 200", w.Code)
	}
}

// quarantineDB quarantines the artifact stored at one path.
type quarantineDB struct {
	*memDB

	blocked string
}

func (q *quarantineDB) IsArtifactQuarantined(_ context.Context, _ string, keys []string) (bool, error) {
	for _, k := range keys {
		if strings.TrimPrefix(k, "/") == q.blocked {
			return true, nil
		}
	}

	return false, nil
}

func TestAnsibleProxy_ServesStaleDocumentsWhenTheUpstreamFails(t *testing.T) {
	g := newFakeGalaxy()
	f := ansibleFixture(t, g)

	listKey := "api/v3/collections/ns/demo/versions/"
	docKey := listKey + "1.0.0/"

	for _, p := range []string{listKey, docKey} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusOK {
			t.Fatalf("priming %s = %d", p, w.Code)
		}

		f.db.expire("repo-1", p)
	}

	f.repo.UpstreamURL = "http://127.0.0.1:1/"

	for _, p := range []string{listKey, "api/v3/collections/ns/demo/", docKey} {
		w := f.do(http.MethodGet, p)
		if w.Code != http.StatusOK || w.Header().Get("Warning") != staleValue {
			t.Errorf("stale %s = %d warning %q", p, w.Code, w.Header().Get("Warning"))
		}
	}
}

func TestAnsibleProxy_UpstreamVersionListProblemsAreNotCached(t *testing.T) {
	t.Run("offset ignored", func(t *testing.T) {
		g := newFakeGalaxy()
		f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
			// Always the first page, whatever the offset.
			r.URL.RawQuery = "limit=2&offset=0"
			g.handler(w, r)
		})
		f.repo.Format, f.repo.Name, f.repo.UpstreamURL = "ansible", "galaxy", f.upstream.URL+"/"

		if w := f.do(http.MethodGet, "api/v3/collections/ns/demo/versions/"); w.Code != http.StatusBadGateway {
			t.Errorf("list from an upstream that ignores offsets = %d, want 502", w.Code)
		}

		if f.db.entry("repo-1", "api/v3/collections/ns/demo/versions/") != nil {
			t.Error("truncated list was cached")
		}
	})

	t.Run("too many versions", func(t *testing.T) {
		g := newFakeGalaxy()
		g.versions = make([]string, ansibleMaxVersions+1)
		for i := range g.versions {
			g.versions[i] = "1.0." + strconv.Itoa(i)
		}

		f := ansibleFixture(t, g)

		if w := f.do(http.MethodGet, "api/v3/collections/ns/demo/versions/"); w.Code != http.StatusBadGateway {
			t.Errorf("oversized list = %d, want 502", w.Code)
		}
	})
}

func TestAnsibleProxy_BlobPolicy(t *testing.T) {
	for key, want := range map[string]bool{
		"api/v3/artifacts/collections/ns-demo-1.0.0.tar.gz": true,
		"api/v3/collections/ns/demo/versions/":              false,
		"api/v3/collections/ns/demo/versions/1.0.0/":        false,
	} {
		if got := Immutable("ansible", key); got != want {
			t.Errorf("Immutable(%q) = %v, want %v", key, got, want)
		}

		if got := isMetadataKey("ansible", key); got == want {
			t.Errorf("isMetadataKey(%q) = %v, want %v", key, got, !want)
		}
	}
}

func TestAnsibleDownloadTarget(t *testing.T) {
	const doc = "https://galaxy.example/api/v3/collections/ns/demo/versions/1.0.0/"

	for in, want := range map[string]string{
		"https://content.example/a.tar.gz#frag": "https://content.example/a.tar.gz",
		"/api/v3/artifacts/a.tar.gz":            "https://galaxy.example/api/v3/artifacts/a.tar.gz",
		"a.tar.gz":                              "https://galaxy.example/api/v3/collections/ns/demo/versions/1.0.0/a.tar.gz",
		"file:///etc/passwd":                    "",
		"":                                      "https://galaxy.example/api/v3/collections/ns/demo/versions/1.0.0/",
	} {
		got, err := ansibleDownloadTarget(doc, in)
		if (err != nil) != (want == "") || got != want && want != "" {
			t.Errorf("ansibleDownloadTarget(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
