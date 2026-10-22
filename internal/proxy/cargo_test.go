// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeCargo is an upstream sparse registry (index host) plus the static host
// its config.json points downloads at. It knows crate "demo" (versions 1.0.0
// and 1.1.0) and "Inflector" (mixed case, index path "in/fl/inflector").
type fakeCargo struct {
	mu sync.Mutex

	files      map[string][]byte // "<crate>/<version>" -> .crate bytes
	digestSrc  map[string][]byte // "<crate>/<version>" -> bytes the index cksum is computed from (default: files)
	dlTemplate string            // overrides the dl advertised in config.json
	rawConfig  string            // overrides the whole config.json
	badIndex   bool              // answer index files with an HTML page
	down       atomic.Bool
	indexPaths []string
	indexAuth  []string
	dlPaths    []string
	dlAuth     []string

	static *httptest.Server
}

func newFakeCargo(t *testing.T) *fakeCargo {
	t.Helper()

	g := &fakeCargo{
		files: map[string][]byte{
			"demo/1.0.0":      []byte("demo 1.0.0 crate"),
			"demo/1.1.0":      []byte("demo 1.1.0 crate"),
			"Inflector/0.1.0": []byte("inflector crate"),
		},
		digestSrc: map[string][]byte{},
	}

	g.static = httptest.NewServer(http.HandlerFunc(g.serveStatic))
	t.Cleanup(g.static.Close)

	return g
}

func (g *fakeCargo) cksum(key string) string {
	src, ok := g.digestSrc[key]
	if !ok {
		src = g.files[key]
	}

	return sum(string(src))
}

func (g *fakeCargo) line(crate, vers string) string {
	b, _ := json.Marshal(map[string]any{
		"name": crate, "vers": vers, "deps": []any{}, "features": map[string]any{},
		"cksum": g.cksum(crate + "/" + vers), "yanked": false,
	})

	return string(b)
}

func (g *fakeCargo) serveStatic(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	g.mu.Lock()
	g.dlPaths = append(g.dlPaths, r.URL.Path)
	g.dlAuth = append(g.dlAuth, r.Header.Get("Authorization"))
	g.mu.Unlock()

	// /crates/<crate>/<version>/download, or the marker form
	// /dl/<prefix>/<crate>/<crate>-<version>.crate.
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")

	switch {
	case len(parts) == 4 && parts[0] == "crates" && parts[3] == "download":
		if b, ok := g.files[parts[1]+"/"+parts[2]]; ok {
			_, _ = w.Write(b)
			return
		}
	case len(parts) == 5 && parts[0] == "dl":
		name, vers, _ := strings.Cut(strings.TrimSuffix(parts[4], ".crate"), "-")
		if b, ok := g.files[parts[3]+"/"+vers]; ok && name == parts[3] {
			_, _ = w.Write(b)
			return
		}
	}

	http.NotFound(w, r)
}

func (g *fakeCargo) handler(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	g.mu.Lock()
	g.indexPaths = append(g.indexPaths, r.URL.Path)
	g.indexAuth = append(g.indexAuth, r.Header.Get("Authorization"))
	bad, rawConfig := g.badIndex, g.rawConfig
	g.mu.Unlock()

	switch r.URL.Path {
	case "/config.json":
		if rawConfig != "" {
			_, _ = io.WriteString(w, rawConfig)
			return
		}

		dl := g.dlTemplate
		if dl == "" {
			dl = g.static.URL + "/crates"
		}

		_, _ = fmt.Fprintf(w, `{"dl":%q,"api":"https://upstream.example"}`, dl)
	case "/de/mo/demo":
		if bad {
			_, _ = io.WriteString(w, "<html>captive portal</html>")
			return
		}

		_, _ = io.WriteString(w, g.line("demo", "1.0.0")+"\n"+g.line("demo", "1.1.0")+"\n")
	case "/in/fl/inflector":
		_, _ = io.WriteString(w, g.line("Inflector", "0.1.0")+"\n")
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeCargo) sawIndex(p string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Contains(g.indexPaths, p)
}

func cargoFixture(t *testing.T) (*fixture, *fakeCargo) {
	t.Helper()

	g := newFakeCargo(t)
	f := newFixture(t, g.handler)
	f.repo.Format = "cargo"
	f.repo.Name = "crates"
	f.repo.UpstreamURL = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	f.svc.SetBaseURL(oaBase + "/")

	return f, g
}

func TestCargoProxy_ConfigPointsAtRepositoryWithoutUpstream(t *testing.T) {
	f, _ := cargoFixture(t)

	for _, path := range []string{"config.json", "index/config.json"} {
		for _, public := range []bool{true, false} {
			f.repo.Public = public

			w := f.do(http.MethodGet, path)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: status %d", path, w.Code)
			}

			var cfg map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
				t.Fatalf("%s: %v", path, err)
			}

			if cfg["dl"] != oaBase+"/repos/crates/api/v1/crates" || cfg["api"] != oaBase+"/repos/crates" {
				t.Errorf("%s: dl/api = %v / %v", path, cfg["dl"], cfg["api"])
			}

			if _, has := cfg["auth-required"]; has == public {
				t.Errorf("%s public=%v: auth-required present = %v", path, public, has)
			}
		}
	}

	if f.hits.Load() != 0 {
		t.Errorf("config.json reached the upstream %d times", f.hits.Load())
	}
}

func TestCargoProxy_IndexIsCachedVerbatimAndPathsFollowTheLayout(t *testing.T) {
	f, g := cargoFixture(t)

	want := g.line("demo", "1.0.0") + "\n" + g.line("demo", "1.1.0") + "\n"

	for range 2 {
		if w := f.do(http.MethodGet, "index/de/mo/demo"); w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("index = %d %q", w.Code, w.Body.String())
		}
	}

	if f.hits.Load() != 1 {
		t.Errorf("upstream requests = %d, want 1", f.hits.Load())
	}

	if !g.sawIndex("/de/mo/demo") {
		t.Errorf("upstream saw %v", g.indexPaths)
	}

	// A conditional request is answered 304 from the cached blob's ETag.
	first := f.do(http.MethodGet, "index/de/mo/demo")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", first.Header().Get("ETag"))

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, "index/de/mo/demo")

	if w.Code != http.StatusNotModified {
		t.Errorf("conditional index request = %d, want 304", w.Code)
	}
}

func TestCargoProxy_RefusesNonCanonicalPathsWithoutUpstreamRequest(t *testing.T) {
	f, _ := cargoFixture(t)

	for _, path := range []string{
		"index/De/Mo/Demo",    // not lowercase
		"index/xx/yy/demo",    // prefix does not match the name
		"index/demo",          // missing prefix
		"index/de/mo/demo/",   // directory
		"index/de/mo/../../x", // traversal
		"api/v1/crates/demo",  // not a download
		"api/v1/crates/new",   // publish path
		"api/v1/crates/d/1/x", // wrong tail
		"api/v1/crates/demo/1.0.0/download/extra",
		"me/demo",
	} {
		if w := f.do(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}

	if f.hits.Load() != 0 {
		t.Errorf("upstream requests = %d, want 0", f.hits.Load())
	}
}

func TestCargoProxy_CrateIsVerifiedCachedAndServedWithUpstreamDown(t *testing.T) {
	f, g := cargoFixture(t)

	const dl = "api/v1/crates/demo/1.0.0/download"

	if w := f.do(http.MethodGet, dl); w.Code != http.StatusOK || w.Body.String() != "demo 1.0.0 crate" {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}

	if len(g.dlPaths) != 1 || g.dlPaths[0] != "/crates/demo/1.0.0/download" {
		t.Errorf("static host saw %v, want the dl template expanded", g.dlPaths)
	}

	e := f.db.entry("repo-1", dl)
	if e == nil || e.BlobKey != g.cksum("demo/1.0.0") {
		t.Fatalf("cache entry = %+v, want blob %s", e, g.cksum("demo/1.0.0"))
	}

	if ttl := e.ExpiresAt.Sub(e.CreatedAt); ttl < 24*3600*365 {
		t.Errorf("crate TTL = %v, want immutable", ttl)
	}

	// Everything upstream goes away and the index expires: the crate is still
	// served (immutable), the index is served stale.
	f.db.expire("repo-1", "index/de/mo/demo")
	g.down.Store(true)

	if w := f.do(http.MethodGet, dl); w.Code != http.StatusOK || w.Body.String() != "demo 1.0.0 crate" {
		t.Errorf("cached crate with upstream down = %d %q", w.Code, w.Body.String())
	}

	w := f.do(http.MethodGet, "index/de/mo/demo")
	if w.Code != http.StatusOK || w.Header().Get("Warning") == "" {
		t.Errorf("stale index = %d, Warning %q; want 200 with a stale warning", w.Code, w.Header().Get("Warning"))
	}

	// A crate never fetched before cannot be served: 502, not a hang.
	if w := f.do(http.MethodGet, "api/v1/crates/demo/1.1.0/download"); w.Code != http.StatusBadGateway {
		t.Errorf("uncached crate with upstream down = %d, want 502", w.Code)
	}
}

func TestCargoProxy_CksumMismatchIsRefusedAndNotCached(t *testing.T) {
	f, g := cargoFixture(t)
	g.digestSrc["demo/1.0.0"] = []byte("some other crate")

	const dl = "api/v1/crates/demo/1.0.0/download"

	if w := f.do(http.MethodGet, dl); w.Code != http.StatusBadGateway {
		t.Errorf("mismatching crate = %d, want 502", w.Code)
	}

	if e := f.db.entry("repo-1", dl); e != nil {
		t.Errorf("mismatching crate was cached: %+v", e)
	}
}

func TestCargoProxy_UnlistedVersionIsNotFoundAndNotFetched(t *testing.T) {
	f, g := cargoFixture(t)

	if w := f.do(http.MethodGet, "api/v1/crates/demo/9.9.9/download"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted version = %d, want 404", w.Code)
	}

	if len(g.dlPaths) != 0 {
		t.Errorf("static host was asked for an unlisted version: %v", g.dlPaths)
	}

	if e := f.db.entry("repo-1", "api/v1/crates/demo/9.9.9/download"); e != nil {
		t.Errorf("unlisted version left a cache entry: %+v", e)
	}

	if w := f.do(http.MethodGet, "api/v1/crates/nope/1.0.0/download"); w.Code != http.StatusNotFound {
		t.Errorf("unknown crate = %d, want 404", w.Code)
	}
}

func TestCargoProxy_MixedCaseCrateNameUsesLowercaseIndex(t *testing.T) {
	f, g := cargoFixture(t)

	w := f.do(http.MethodGet, "api/v1/crates/Inflector/0.1.0/download")
	if w.Code != http.StatusOK || w.Body.String() != "inflector crate" {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}

	if !g.sawIndex("/in/fl/inflector") {
		t.Errorf("index paths = %v, want the lowercase index file", g.indexPaths)
	}

	if len(g.dlPaths) != 1 || g.dlPaths[0] != "/crates/Inflector/0.1.0/download" {
		t.Errorf("download path = %v, want the crate name as the client wrote it", g.dlPaths)
	}
}

func TestCargoProxy_DownloadTemplateMarkers(t *testing.T) {
	f, g := cargoFixture(t)
	g.dlTemplate = g.static.URL + "/dl/{lowerprefix}/{crate}/{crate}-{version}.crate"

	if w := f.do(http.MethodGet, "api/v1/crates/demo/1.1.0/download"); w.Code != http.StatusOK || w.Body.String() != "demo 1.1.0 crate" {
		t.Fatalf("download = %d %q", w.Code, w.Body.String())
	}

	if len(g.dlPaths) != 1 || g.dlPaths[0] != "/dl/de/mo/demo/demo-1.1.0.crate" {
		t.Errorf("static host saw %v", g.dlPaths)
	}
}

func TestCargoProxy_CredentialsStayOnTheUpstreamOrigin(t *testing.T) {
	f, g := cargoFixture(t)
	f.repo.UpstreamUsername = "svc"

	if w := f.do(http.MethodGet, "api/v1/crates/demo/1.0.0/download"); w.Code != http.StatusOK {
		t.Fatalf("download = %d", w.Code)
	}

	for _, a := range g.indexAuth {
		if !strings.HasPrefix(a, "Basic ") {
			t.Errorf("index request carried Authorization %q, want Basic credentials", a)
		}
	}

	for _, a := range g.dlAuth {
		if a != "" {
			t.Errorf("credentials leaked to the download host: %q", a)
		}
	}
}

func TestCargoProxy_ConfigWithoutDlIsAnUpstreamError(t *testing.T) {
	f, g := cargoFixture(t)
	g.rawConfig = `{"api":"https://upstream.example"}`

	const dl = "api/v1/crates/demo/1.0.0/download"

	if w := f.do(http.MethodGet, dl); w.Code != http.StatusBadGateway {
		t.Errorf("download with an unusable upstream config = %d, want 502", w.Code)
	}

	if e := f.db.entry("repo-1", dl); e != nil {
		t.Errorf("crate was cached: %+v", e)
	}

	if len(g.dlPaths) != 0 {
		t.Errorf("static host was asked: %v", g.dlPaths)
	}
}

func TestCargoProxy_GarbageIndexIsRefusedAndNotCached(t *testing.T) {
	f, g := cargoFixture(t)
	g.badIndex = true

	if w := f.do(http.MethodGet, "index/de/mo/demo"); w.Code != http.StatusBadGateway {
		t.Errorf("html index = %d, want 502", w.Code)
	}

	if e := f.db.entry("repo-1", "index/de/mo/demo"); e != nil {
		t.Errorf("garbage index was cached: %+v", e)
	}
}

func TestCargoProxy_WritesAreRejected(t *testing.T) {
	f, _ := cargoFixture(t)

	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPost} {
		w := f.do(method, "api/v1/crates/new")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s = %d Allow %q, want 405 GET, HEAD", method, w.Code, w.Header().Get("Allow"))
		}
	}
}

func TestCargoImmutable(t *testing.T) {
	for key, want := range map[string]bool{
		"api/v1/crates/demo/1.0.0/download": true,
		"index/de/mo/demo":                  false,
		"config.json":                       false,
	} {
		if got := Immutable("cargo", key); got != want {
			t.Errorf("Immutable(cargo, %q) = %v, want %v", key, got, want)
		}
	}
}
