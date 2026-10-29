// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

const composerBase = "https://oa.example"

// fakePackagist emulates repo.packagist.org (p2 metadata) with its dists on a
// separate host, as GitHub's API and codeload are.
type fakePackagist struct {
	dists    *httptest.Server
	distAuth atomic.Value
	distHits atomic.Int32
	files    map[string]string // dist path -> bytes
	shasum   map[string]string // version -> shasum published ("" = none)
	zipball  bool              // serve the dist of 1.0.0 as a redirect, like GitHub
}

func newFakePackagist(t *testing.T) *fakePackagist {
	t.Helper()

	p := &fakePackagist{
		files: map[string]string{
			"/zip/1.0.0":    "lib 1.0.0 bytes",
			"/zip/1.1.0":    "lib 1.1.0 bytes",
			"/zip/dev-main": "lib dev-main bytes",
		},
		shasum: map[string]string{"1.0.0": composerSHA1("lib 1.0.0 bytes"), "1.1.0": "", "dev-main": ""},
	}

	p.dists = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.distAuth.Store(r.Header.Get("Authorization"))
		p.distHits.Add(1)

		if rest, ok := strings.CutPrefix(r.URL.Path, "/codeload"); ok {
			r.URL.Path = rest
		} else if p.zipball && r.URL.Path == "/zip/1.0.0" {
			http.Redirect(w, r, "/codeload/zip/1.0.0", http.StatusFound)
			return
		}

		body, ok := p.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(p.dists.Close)

	return p
}

func composerSHA1(s string) string {
	h := sha1.Sum([]byte(s))

	return hex.EncodeToString(h[:])
}

func (p *fakePackagist) version(v, normalized string) pkgcomposer.Version {
	return pkgcomposer.Version{
		"name": "acme/lib", "version": v, "version_normalized": normalized, "require": map[string]any{"php": ">=8.1"},
		"source": map[string]any{"type": "git", "url": "https://github.com/acme/lib.git", "reference": "ref-" + v},
		"dist": map[string]any{
			"type": "zip", "url": p.dists.URL + "/zip/" + v, "reference": "ref-" + v, "shasum": p.shasum[v],
		},
		"type": "library",
	}
}

func (p *fakePackagist) handler(w http.ResponseWriter, r *http.Request) {
	var versions []pkgcomposer.Version

	switch r.URL.Path {
	case "/p2/acme/lib.json":
		versions = []pkgcomposer.Version{p.version("1.1.0", "1.1.0.0"), p.version("1.0.0", "1.0.0.0")}
	case "/p2/acme/lib~dev.json":
		versions = []pkgcomposer.Version{p.version("dev-main", "dev-main")}
	default:
		http.NotFound(w, r)
		return
	}

	body, err := pkgcomposer.EncodeDocument(map[string][]pkgcomposer.Version{"acme/lib": versions}, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func composerFixture(t *testing.T, p *fakePackagist) *fixture {
	t.Helper()

	f := newFixture(t, p.handler)
	f.repo.Format = "composer"
	f.repo.Name = "packagist"
	f.repo.UpstreamURL = f.upstream.URL + "/"
	f.svc.baseURL = composerBase

	return f
}

// composerServed decodes a served p2 document into its expanded versions.
func composerServed(t *testing.T, body []byte) []pkgcomposer.Version {
	t.Helper()

	pkgs, err := pkgcomposer.Decode(body)
	if err != nil {
		t.Fatalf("served p2 is not a document: %v\n%s", err, body)
	}

	return pkgs["acme/lib"]
}

// distPath is the repository-relative path of a served dist.
func distPath(t *testing.T, v pkgcomposer.Version) string {
	t.Helper()

	d, ok := v.ZipDist()
	if !ok {
		t.Fatalf("version without a dist: %v", v)
	}

	rest, ok := strings.CutPrefix(d.URL, composerBase+"/repos/packagist/")
	if !ok {
		t.Fatalf("dist url %q does not point at the repository", d.URL)
	}

	return rest
}

func TestComposerProxy_RootIsGeneratedAndOwnedByTheProxy(t *testing.T) {
	f := composerFixture(t, newFakePackagist(t))

	w := f.do(http.MethodGet, "packages.json")
	if w.Code != http.StatusOK {
		t.Fatalf("packages.json = %d", w.Code)
	}

	if body := w.Body.String(); !strings.Contains(body, `"metadata-url":"`+composerBase+`/repos/packagist/p2/%package%.json"`) ||
		strings.Contains(body, "available-packages") || strings.Contains(body, f.upstream.URL) {
		t.Errorf("packages.json = %s", body)
	}

	if f.hits.Load() != 0 {
		t.Errorf("packages.json reached the upstream %d times", f.hits.Load())
	}
}

func TestComposerProxy_MetadataIsRelinkedKeepsSourceAndRevalidates(t *testing.T) {
	p := newFakePackagist(t)
	f := composerFixture(t, p)

	w := f.do(http.MethodGet, "p2/acme/lib.json")
	if w.Code != http.StatusOK {
		t.Fatalf("p2 = %d %s", w.Code, w.Body.String())
	}

	versions := composerServed(t, w.Body.Bytes())
	if len(versions) != 2 || versions[0].VersionString() != "1.1.0" || versions[1].VersionString() != "1.0.0" {
		t.Fatalf("versions = %v", versions)
	}

	if strings.Contains(w.Body.String(), p.dists.URL) {
		t.Errorf("a dist url still points at the dist host: %s", w.Body.String())
	}

	d, _ := versions[1].ZipDist()
	if d.SHA1 != composerSHA1("lib 1.0.0 bytes") || d.Reference != "ref-1.0.0" {
		t.Errorf("shasum or reference changed: %+v", d)
	}

	if src, _ := versions[1]["source"].(map[string]any); src["url"] != "https://github.com/acme/lib.git" {
		t.Errorf("source was touched: %v", versions[1]["source"])
	}

	if f.db.entry("repo-1", "p2/acme/lib.json") == nil {
		t.Error("upstream document is not cached under its repository-relative key")
	}

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	hits := f.hits.Load()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	w2 := httptest.NewRecorder()
	f.svc.Serve(w2, req, f.repo, "p2/acme/lib.json")

	if w2.Code != http.StatusNotModified || f.hits.Load() != hits {
		t.Errorf("revalidation = %d with %d new upstream requests", w2.Code, f.hits.Load()-hits)
	}

	// The cache is upstream-shaped: another server URL changes the ETag.
	f.svc.baseURL = "https://other.example"

	if w3 := f.do(
		http.MethodGet,
		"p2/acme/lib.json",
	); w3.Header().Get("ETag") == etag ||
		!strings.Contains(w3.Body.String(), "other.example") {
		t.Errorf("rendered document did not follow the server URL: %s", w3.Body.String())
	}
}

func TestComposerProxy_DistIsVerifiedCachedOnceAndServedWithTheUpstreamDown(t *testing.T) {
	p := newFakePackagist(t)
	f := composerFixture(t, p)

	versions := composerServed(t, f.do(http.MethodGet, "p2/acme/lib.json").Body.Bytes())
	path := distPath(t, versions[1])

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != "lib 1.0.0 bytes" {
		t.Fatalf("dist = %d %q", w.Code, w.Body.String())
	}

	if e := f.db.entry("repo-1", path); e == nil || e.BlobKey != sum("lib 1.0.0 bytes") {
		t.Fatalf("dist not cached content-addressed: %+v", e)
	}

	// Metadata and dist are served with the upstream gone.
	f.repo.UpstreamURL = "http://127.0.0.1:1/"
	p.dists.Close()

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != "lib 1.0.0 bytes" {
		t.Errorf("cached dist = %d %q", w.Code, w.Body.String())
	}

	f.db.expire("repo-1", "p2/acme/lib.json")

	w := f.do(http.MethodGet, "p2/acme/lib.json")
	if w.Code != http.StatusOK || w.Header().Get("Warning") != staleValue {
		t.Errorf("stale p2 = %d warning %q", w.Code, w.Header().Get("Warning"))
	}
}

func TestComposerProxy_DistFollowsRedirectsOfTheDistHost(t *testing.T) {
	p := newFakePackagist(t)
	p.zipball = true
	f := composerFixture(t, p)

	versions := composerServed(t, f.do(http.MethodGet, "p2/acme/lib.json").Body.Bytes())

	if w := f.do(http.MethodGet, distPath(t, versions[1])); w.Code != http.StatusOK || w.Body.String() != "lib 1.0.0 bytes" {
		t.Fatalf("redirected dist = %d %q", w.Code, w.Body.String())
	}
}

func TestComposerProxy_DistWithWrongShasumIsRefusedAndNotCached(t *testing.T) {
	p := newFakePackagist(t)
	p.files["/zip/1.0.0"] = "tampered"
	f := composerFixture(t, p)

	versions := composerServed(t, f.do(http.MethodGet, "p2/acme/lib.json").Body.Bytes())
	path := distPath(t, versions[1])

	for range 2 {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusBadGateway {
			t.Fatalf("mismatching dist = %d %q, want 502", w.Code, w.Body.String())
		}
	}

	if f.db.entry("repo-1", path) != nil {
		t.Error("a dist that does not match its shasum was cached")
	}

	// A version without a published shasum is vouched for by its reference only.
	if w := f.do(http.MethodGet, distPath(t, versions[0])); w.Code != http.StatusOK || w.Body.String() != "lib 1.1.0 bytes" {
		t.Errorf("dist without shasum = %d %q", w.Code, w.Body.String())
	}
}

func TestComposerProxy_DistHostGetsNoCredentials(t *testing.T) {
	p := newFakePackagist(t)
	f := composerFixture(t, p)
	f.repo.UpstreamUsername = "robot"

	versions := composerServed(t, f.do(http.MethodGet, "p2/acme/lib.json").Body.Bytes())

	if w := f.do(http.MethodGet, distPath(t, versions[1])); w.Code != http.StatusOK {
		t.Fatalf("dist = %d", w.Code)
	}

	if auth, _ := p.distAuth.Load().(string); auth != "" {
		t.Errorf("dist host received credentials: %q", auth)
	}
}

func TestComposerProxy_ForgedOrUnpublishedDistsAreNotFetched(t *testing.T) {
	p := newFakePackagist(t)
	f := composerFixture(t, p)

	versions := composerServed(t, f.do(http.MethodGet, "p2/acme/lib.json").Body.Bytes())
	good := distPath(t, versions[1])
	parts := strings.Split(good, "/")

	forged := strings.Join(append(append([]string{}, parts[:len(parts)-1]...), strings.Repeat("a", 64)+".zip"), "/")
	other := strings.Replace(good, "/1.0.0/", "/9.9.9/", 1)
	pkg := strings.Replace(good, "acme/lib", "acme/other", 1)

	before := p.distHits.Load()

	for _, path := range []string{forged, other, pkg, "dists/acme/lib/1.0.0/x.zip", "list/acme.json", "search.json"} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}

	if p.distHits.Load() != before {
		t.Errorf("the dist host was asked for %d forged paths", p.distHits.Load()-before)
	}
}

func TestComposerProxy_DevVersionsComeFromTheDevDocument(t *testing.T) {
	p := newFakePackagist(t)
	f := composerFixture(t, p)

	w := f.do(http.MethodGet, "p2/acme/lib~dev.json")
	if w.Code != http.StatusOK {
		t.Fatalf("dev p2 = %d", w.Code)
	}

	versions := composerServed(t, w.Body.Bytes())
	if len(versions) != 1 || versions[0].VersionString() != "dev-main" {
		t.Fatalf("dev versions = %v", versions)
	}

	if w := f.do(http.MethodGet, distPath(t, versions[0])); w.Code != http.StatusOK || w.Body.String() != "lib dev-main bytes" {
		t.Errorf("dev dist = %d %q", w.Code, w.Body.String())
	}
}

func TestComposerProxy_UpstreamNotAPackageDocumentIsRefused(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>captive portal</html>"))
	})
	f.repo.Format, f.repo.Name, f.repo.UpstreamURL = "composer", "packagist", f.upstream.URL+"/"
	f.svc.baseURL = composerBase

	if w := f.do(http.MethodGet, "p2/acme/lib.json"); w.Code != http.StatusBadGateway {
		t.Errorf("html as p2 = %d, want 502", w.Code)
	}

	if f.db.entry("repo-1", "p2/acme/lib.json") != nil {
		t.Error("an unusable document was cached")
	}
}

func TestComposerProxy_BlobPolicy(t *testing.T) {
	for key, want := range map[string]bool{
		"dists/acme/lib/1.0.0/abc.zip":     true,
		"dists/acme/lib/v1.2.3/abc.zip":    true,
		"dists/acme/lib/dev-main/abc.zip":  false,
		"dists/acme/lib/1.0.x-dev/abc.zip": false,
		"dists/acme/lib/nonsense/abc.zip":  false,
		"p2/acme/lib.json":                 false,
	} {
		if got := Immutable("composer", key); got != want {
			t.Errorf("Immutable(%q) = %v, want %v", key, got, want)
		}
	}

	if isMetadataKey("composer", "dists/acme/lib/1.0.0/abc.zip") || !isMetadataKey("composer", "p2/acme/lib.json") {
		t.Error("isMetadataKey must cap p2 documents and not dists")
	}
}
