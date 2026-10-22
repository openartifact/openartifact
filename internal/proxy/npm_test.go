// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/format/base"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

const oaBase = "http://oa.test"

func sriOf(b []byte) string {
	h := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(h[:])
}

func shasumOf(b []byte) string {
	h := sha1.Sum(b)
	return hex.EncodeToString(h[:])
}

// fakeRegistry is an upstream npm registry with two packages ("lib" and
// "@s/name"), each with one version whose tarball lives on tarballHost.
type fakeRegistry struct {
	mu sync.Mutex

	tarballHost string // base URL tarballs are advertised under
	tarballs    map[string][]byte
	digestSrc   map[string][]byte // tarball file -> bytes the advertised digests are computed from (default: the served bytes)
	omitSRI     bool              // advertise dist.shasum only
	noDigest    bool              // advertise neither
	down        atomic.Bool       // answer every request with 503
	paths       []string          // escaped request paths seen
	accepts     []string          // Accept headers seen on packument requests
	auths       []string          // Authorization headers seen, per request
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		tarballs: map[string][]byte{
			"lib-1.0.0.tgz":  []byte("lib tarball bytes"),
			"name-2.0.0.tgz": []byte("scoped tarball bytes"),
		},
		digestSrc: map[string][]byte{},
	}
}

func (g *fakeRegistry) packument(name, version, file string) string {
	src, ok := g.digestSrc[file]
	if !ok {
		src = g.tarballs[file]
	}

	dist := map[string]string{"tarball": g.tarballHost + "/" + name + "/-/" + file}
	if !g.noDigest {
		dist["shasum"] = shasumOf(src)

		if !g.omitSRI {
			dist["integrity"] = sriOf(src)
		}
	}

	b, _ := json.Marshal(map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{version: map[string]any{"name": name, "version": version, "dist": dist}},
	})

	return string(b)
}

func (g *fakeRegistry) handler(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	g.mu.Lock()
	g.paths = append(g.paths, r.URL.EscapedPath())
	g.auths = append(g.auths, r.Header.Get("Authorization"))

	if !strings.Contains(r.URL.Path, "/-/") {
		g.accepts = append(g.accepts, r.Header.Get("Accept"))
	}

	g.mu.Unlock()

	switch r.URL.Path {
	case "/lib":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, g.packument("lib", "1.0.0", "lib-1.0.0.tgz"))
	case "/@s/name":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, g.packument("@s/name", "2.0.0", "name-2.0.0.tgz"))
	case "/lib/-/lib-1.0.0.tgz":
		_, _ = w.Write(g.tarballs["lib-1.0.0.tgz"])
	case "/@s/name/-/name-2.0.0.tgz":
		_, _ = w.Write(g.tarballs["name-2.0.0.tgz"])
	case "/lib/-/lib-9.9.9.tgz":
		_, _ = io.WriteString(w, "not advertised")
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeRegistry) sawPath(p string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Contains(g.paths, p)
}

// npmFixture serves the fake registry through an npm proxy repository.
func npmFixture(t *testing.T) (*fixture, *fakeRegistry) {
	t.Helper()

	reg := newFakeRegistry()
	f := newFixture(t, reg.handler)
	reg.tarballHost = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	f.repo.Format = "npm"
	f.repo.Name = "npmjs"
	f.repo.UpstreamURL = reg.tarballHost
	f.svc.SetBaseURL(oaBase + "/")

	return f, reg
}

func (f *fixture) doWith(path string, header map[string]string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	return w
}

type npmDoc struct {
	DistTags map[string]string `json:"dist-tags"` //nolint:tagliatelle // npm registry wire format
	Versions map[string]struct {
		Dist pkgnpm.Dist `json:"dist"`
	} `json:"versions"`
}

func decodeDoc(t *testing.T, w *httptest.ResponseRecorder) npmDoc {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var d npmDoc
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("not a packument: %v\n%s", err, w.Body.String())
	}

	return d
}

func TestNPMProxy_PackumentTarballURLsAreRewrittenAndCached(t *testing.T) {
	f, reg := npmFixture(t)

	for _, tc := range []struct{ path, version, wantTarball string }{
		{"lib", "1.0.0", oaBase + "/repos/npmjs/lib/-/lib-1.0.0.tgz"},
		{"@s%2fname", "2.0.0", oaBase + "/repos/npmjs/@s/name/-/name-2.0.0.tgz"},
		{"@s/name", "2.0.0", oaBase + "/repos/npmjs/@s/name/-/name-2.0.0.tgz"},
	} {
		d := decodeDoc(t, f.do(http.MethodGet, tc.path))

		if got := d.Versions[tc.version].Dist.Tarball; got != tc.wantTarball {
			t.Errorf("%s: tarball = %q, want %q", tc.path, got, tc.wantTarball)
		}

		if d.Versions[tc.version].Dist.Integrity == "" || d.Versions[tc.version].Dist.Shasum == "" {
			t.Errorf("%s: integrity fields were lost: %+v", tc.path, d.Versions[tc.version].Dist)
		}
	}

	if !reg.sawPath("/@s%2Fname") {
		t.Errorf("scoped packument was not requested with an escaped scope separator, saw %v", reg.paths)
	}

	// lib once, @s/name once (the second scoped request is a cache hit).
	if got := f.hits.Load(); got != 2 {
		t.Errorf("upstream requests = %d, want 2", got)
	}
}

func TestNPMProxy_AbbreviatedAndFullPackumentsCacheSeparately(t *testing.T) {
	f, reg := npmFixture(t)

	abbr := map[string]string{"Accept": "application/vnd.npm.install-v1+json"}

	f.doWith("lib", abbr)
	f.doWith("lib", nil)
	f.doWith("lib", abbr)
	f.doWith("lib", nil)

	if got := f.hits.Load(); got != 2 {
		t.Errorf("upstream requests = %d, want 2 (one per variant)", got)
	}

	if len(reg.accepts) != 2 || !strings.Contains(reg.accepts[0], pkgnpm.AbbreviatedContentType) || strings.Contains(reg.accepts[1], "vnd.npm") {
		t.Errorf("upstream Accept headers = %q, want abbreviated then plain JSON", reg.accepts)
	}
}

func TestNPMProxy_TarballIsVerifiedCachedAndServedWithUpstreamDown(t *testing.T) {
	f, reg := npmFixture(t)

	for _, tc := range []struct{ path, want string }{
		{"lib/-/lib-1.0.0.tgz", "lib tarball bytes"},
		{"@s%2fname/-/name-2.0.0.tgz", "scoped tarball bytes"},
		{"@s/name/-/name-2.0.0.tgz", "scoped tarball bytes"},
	} {
		if w := f.do(http.MethodGet, tc.path); w.Code != http.StatusOK || w.Body.String() != tc.want {
			t.Fatalf("GET %s = %d %q", tc.path, w.Code, w.Body.String())
		}
	}

	// Upstream goes away. Tarballs are immutable and keep being served; the
	// expired packument is served stale (within max_stale_seconds).
	f.db.expire("repo-1", "lib")
	reg.down.Store(true)

	if w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusOK || w.Body.String() != "lib tarball bytes" {
		t.Errorf("cached tarball with upstream down = %d %q", w.Code, w.Body.String())
	}

	w := f.do(http.MethodGet, "lib")
	if w.Code != http.StatusOK || w.Header().Get("Warning") == "" {
		t.Errorf("stale packument = %d, Warning %q; want 200 with a stale warning", w.Code, w.Header().Get("Warning"))
	}

	if d := decodeDoc(t, w); d.Versions["1.0.0"].Dist.Tarball != oaBase+"/repos/npmjs/lib/-/lib-1.0.0.tgz" {
		t.Errorf("stale packument was not rewritten: %+v", d.Versions["1.0.0"].Dist)
	}
}

func TestNPMProxy_IntegrityMismatchIsRefusedAndNotCached(t *testing.T) {
	genuine := []byte("lib tarball bytes")

	for name, setup := range map[string]func(*fakeRegistry){
		// The packument advertises another tarball's digest.
		"integrity": func(g *fakeRegistry) { g.digestSrc["lib-1.0.0.tgz"] = []byte("something else") },
		// Legacy version with only dist.shasum; the served bytes were swapped.
		"shasum": func(g *fakeRegistry) {
			g.omitSRI = true
			g.digestSrc["lib-1.0.0.tgz"] = genuine
			g.tarballs["lib-1.0.0.tgz"] = []byte("tampered")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, reg := npmFixture(t)
			setup(reg)

			w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz")
			if w.Code != http.StatusBadGateway {
				t.Fatalf("mismatching tarball = %d, want 502", w.Code)
			}

			if strings.Contains(w.Body.String(), "bytes") || strings.Contains(w.Body.String(), "tampered") {
				t.Error("refused tarball content leaked into the response")
			}

			if e := f.db.entry("repo-1", "lib/-/lib-1.0.0.tgz"); e != nil {
				t.Errorf("mismatching tarball was cached: %+v", e)
			}
		})
	}
}

func TestNPMProxy_TarballWithoutDigestIsAccepted(t *testing.T) {
	f, reg := npmFixture(t)
	reg.noDigest = true

	if w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("tarball of a version without digests = %d, want 200", w.Code)
	}
}

func TestNPMProxy_TarballNotListedInPackumentIsNotFetched(t *testing.T) {
	f, reg := npmFixture(t)

	if w := f.do(http.MethodGet, "lib/-/lib-9.9.9.tgz"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted tarball = %d, want 404", w.Code)
	}

	if reg.sawPath("/lib/-/lib-9.9.9.tgz") {
		t.Error("an arbitrary tarball path was fetched from upstream")
	}

	if e := f.db.entry("repo-1", "lib/-/lib-9.9.9.tgz"); e != nil {
		t.Errorf("a negative entry was recorded for an unlisted tarball: %+v", e)
	}
}

func TestNPMProxy_VersionManifestIsCutFromThePackument(t *testing.T) {
	f, _ := npmFixture(t)

	for _, path := range []string{"lib/1.0.0", "lib/latest"} {
		w := f.do(http.MethodGet, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}

		var v struct {
			Version string      `json:"version"`
			Dist    pkgnpm.Dist `json:"dist"`
		}

		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.Version != "1.0.0" || v.Dist.Tarball != oaBase+"/repos/npmjs/lib/-/lib-1.0.0.tgz" {
			t.Errorf("GET %s = %s (%v)", path, w.Body.String(), err)
		}
	}

	if w := f.do(http.MethodGet, "lib/3.0.0"); w.Code != http.StatusNotFound {
		t.Errorf("unknown version = %d, want 404", w.Code)
	}
}

func TestNPMProxy_PackumentRevalidatesAndRejectsWrites(t *testing.T) {
	f, _ := npmFixture(t)

	etag := f.do(http.MethodGet, "lib").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	if w := f.doWith("lib", map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}

	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequestWithContext(context.Background(), m, "/", nil)
		w := httptest.NewRecorder()
		f.svc.Serve(w, req, f.repo, "lib")

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405 (publish/unpublish to a proxy)", m, w.Code)
		}
	}
}

func TestNPMProxy_UnknownPackageAndGarbageUpstream(t *testing.T) {
	f, _ := npmFixture(t)

	if w := f.do(http.MethodGet, "nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown package = %d, want 404", w.Code)
	}

	for _, p := range []string{"-/v1/search", "lib/-/", "lib/a/b/c", "../etc", "@s"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d, want 404", p, w.Code)
		}
	}

	g := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>captive portal</html>") })
	g.repo.Format = "npm"
	g.svc.SetBaseURL(oaBase)

	if w := g.do(http.MethodGet, "lib"); w.Code != http.StatusBadGateway {
		t.Errorf("non-JSON upstream answer = %d, want 502", w.Code)
	}

	if e := g.db.entry("repo-1", "lib"); e != nil {
		t.Errorf("non-JSON answer was cached: %+v", e)
	}
}

func TestNPMProxy_CredentialsOnlyGoToTheConfiguredUpstream(t *testing.T) {
	f, reg := npmFixture(t)
	f.repo.UpstreamUsername = "ci"

	// Tarballs are advertised on a second server, as a CDN would.
	cdn := newFakeRegistry()
	cdnServer := httptest.NewServer(http.HandlerFunc(cdn.handler))
	t.Cleanup(cdnServer.Close)

	cdn.tarballs = reg.tarballs
	reg.tarballHost = cdnServer.URL

	if w := f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("tarball from the CDN host = %d", w.Code)
	}

	if len(reg.auths) == 0 || reg.auths[0] == "" {
		t.Error("the packument request carried no upstream credentials")
	}

	if len(cdn.auths) != 1 || cdn.auths[0] != "" {
		t.Errorf("credentials sent to a host a packument points at: %q", cdn.auths)
	}
}

// Rendering a packument holds one of the bounded in-memory slots: with every
// slot taken the request waits (and gives up with its context) instead of
// reading yet another whole document.
func TestNPMProxy_PackumentRenderWaitsForASlot(t *testing.T) {
	f, _ := npmFixture(t)

	if w := f.do(http.MethodGet, "lib"); w.Code != http.StatusOK {
		t.Fatalf("priming: status %d", w.Code)
	}

	f.svc.packuments = base.NewGate(1)
	if err := f.svc.packuments.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, "lib")

	if w.Body.Len() != 0 {
		t.Errorf("rendered %d bytes with every slot taken", w.Body.Len())
	}

	f.svc.packuments.Release()

	if w := f.do(http.MethodGet, "lib"); w.Code != http.StatusOK {
		t.Errorf("after release: status %d", w.Code)
	}
}
