// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const nugetRepoURL = oaBase + "/repos/nugetorg"

// buildNupkg returns a .nupkg (zip with a root nuspec) for id/version.
func buildNupkg(t *testing.T, id, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for name, body := range map[string]string{
		id + ".nuspec":              `<?xml version="1.0"?><package><metadata><id>` + id + `</id><version>` + version + `</version></metadata></package>`,
		"lib/net8.0/" + id + ".dll": "binary",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = io.WriteString(w, body)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func sha512b64(b []byte) string {
	h := sha512.Sum512(b)
	return base64.StdEncoding.EncodeToString(h[:])
}

func gz(t *testing.T, s string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	_, _ = io.WriteString(zw, s)

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// nugetFeed is an upstream v3 feed: package "lib.a" 1.0.0 with an inline
// registration, package "big" with paged registrations, a catalog and search.
// Its registration documents are gzipped, as nuget.org serves them.
type nugetFeed struct {
	t *testing.T

	base string // origin the feed is served on
	cdn  string // origin the flat container is advertised on

	nupkg []byte // lib.a 1.0.0

	// catalog tweaks
	noCatalog   bool   // the catalog entry URL answers 404
	noLeaf      bool   // the registration leaf answers 404
	hashOf      []byte // bytes the catalog hash is computed from (default nupkg)
	catalogSize int64  // packageSize (default len(nupkg))
	noHash      bool   // catalog entry without hash and size

	down atomic.Bool

	mu    sync.Mutex
	paths []string
	auths map[string][]string // path -> Authorization headers
}

func (g *nugetFeed) seen(p string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return strings.Count(strings.Join(g.paths, "\n")+"\n", p+"\n")
}

func (g *nugetFeed) serviceIndex() string {
	return fmt.Sprintf(`{"version":"3.0.0","resources":[
 {"@id":%[1]q,"@type":"PackageBaseAddress/3.0.0"},
 {"@id":"%[2]s/reg1/","@type":"RegistrationsBaseUrl"},
 {"@id":"%[2]s/reg/","@type":"RegistrationsBaseUrl/3.6.0"},
 {"@id":"%[2]s/search/query","@type":"SearchQueryService"},
 {"@id":"%[2]s/search/autocomplete","@type":"SearchAutocompleteService"},
 {"@id":"%[2]s/publish","@type":"PackagePublish/2.0.0"},
 {"@id":"%[2]s/catalog/index.json","@type":"Catalog/3.0.0"}
]}`, g.cdn+"/flat/", g.base)
}

func (g *nugetFeed) leaf(id, version string) string {
	return fmt.Sprintf(`{"@id":"%[1]s/reg/%[2]s/%[3]s.json","catalogEntry":"%[1]s/catalog/%[2]s.%[3]s.json","listed":true,`+
		`"packageContent":"%[4]s/flat/%[2]s/%[3]s/%[2]s.%[3]s.nupkg","registration":"%[1]s/reg/%[2]s/index.json"}`, g.base, id, version, g.cdn)
}

func (g *nugetFeed) item(id, version string) string {
	return fmt.Sprintf(`{"@id":"%[1]s/reg/%[2]s/%[3]s.json","@type":"Package","catalogEntry":{"@id":"%[1]s/catalog/%[2]s.%[3]s.json",`+
		`"id":%[2]q,"version":%[3]q,"packageContent":"%[4]s/flat/%[2]s/%[3]s/%[2]s.%[3]s.nupkg","description":"see https://api.nuget.org/v3-flatcontainer/ & <more>"},`+
		`"packageContent":"%[4]s/flat/%[2]s/%[3]s/%[2]s.%[3]s.nupkg","registration":"%[1]s/reg/%[2]s/index.json"}`, g.base, id, version, g.cdn)
}

func (g *nugetFeed) catalog() string {
	if g.noHash {
		return `{"id":"lib.a","version":"1.0.0"}`
	}

	src := g.hashOf
	if src == nil {
		src = g.nupkg
	}

	size := g.catalogSize
	if size == 0 {
		size = int64(len(g.nupkg))
	}

	return fmt.Sprintf(
		`{"id":"lib.a","version":"1.0.0","packageHash":%q,"packageHashAlgorithm":"SHA512","packageSize":%d}`,
		sha512b64(src),
		size,
	)
}

func (g *nugetFeed) handler(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	g.mu.Lock()
	g.paths = append(g.paths, r.URL.Path)

	if g.auths == nil {
		g.auths = map[string][]string{}
	}

	g.auths[r.URL.Path] = append(g.auths[r.URL.Path], r.Header.Get("Authorization"))
	g.mu.Unlock()

	json200 := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	gz200 := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gz(g.t, body))
	}

	switch r.URL.Path {
	case "/v3/index.json":
		json200(g.serviceIndex())
	case "/flat/lib.a/index.json":
		json200(`{"versions":["1.0.0"]}`)
	case "/flat/lib.a/1.0.0/lib.a.1.0.0.nupkg":
		_, _ = w.Write(g.nupkg)
	case "/flat/lib.a/1.0.0/lib.a.nuspec":
		_, _ = io.WriteString(w, "\ufeff<?xml version=\"1.0\"?><package><metadata><id>lib.a</id></metadata></package>")
	case "/reg/lib.a/index.json":
		gz200(fmt.Sprintf(`{"@id":"%[1]s/reg/lib.a/index.json","count":1,"items":[{"@id":"%[1]s/reg/lib.a/index.json#page/1.0.0/1.0.0",`+
			`"count":1,"lower":"1.0.0","upper":"1.0.0","parent":"%[1]s/reg/lib.a/index.json","items":[%[2]s]}]}`, g.base, g.item("lib.a", "1.0.0")))
	case "/reg/lib.a/1.0.0.json":
		if g.noLeaf {
			http.NotFound(w, r)
			return
		}

		gz200(g.leaf("lib.a", "1.0.0"))
	case "/catalog/lib.a.1.0.0.json":
		if g.noCatalog {
			http.NotFound(w, r)
			return
		}

		json200(g.catalog())
	case "/reg/big/index.json":
		gz200(fmt.Sprintf(`{"@id":"%[1]s/reg/big/index.json","count":2,"items":[`+
			`{"@id":"%[1]s/reg/big/page/1.0.0/2.0.0.json","count":2,"lower":"1.0.0","upper":"2.0.0","parent":"%[1]s/reg/big/index.json"},`+
			`{"@id":"%[1]s/reg/big/page/3.0.0/3.0.0.json","count":1,"lower":"3.0.0","upper":"3.0.0","parent":"%[1]s/reg/big/index.json"}]}`, g.base))
	case "/reg/big/page/1.0.0/2.0.0.json":
		gz200(
			fmt.Sprintf(
				`{"@id":"%[1]s/reg/big/page/1.0.0/2.0.0.json","count":2,"lower":"1.0.0","upper":"2.0.0","parent":"%[1]s/reg/big/index.json","items":[%[2]s,%[3]s]}`,
				g.base,
				g.item("big", "1.0.0"),
				g.item("big", "2.0.0"),
			),
		)
	case "/reg/big/page/3.0.0/3.0.0.json":
		gz200(
			fmt.Sprintf(
				`{"@id":"%[1]s/reg/big/page/3.0.0/3.0.0.json","count":1,"lower":"3.0.0","upper":"3.0.0","parent":"%[1]s/reg/big/index.json","items":[%[2]s]}`,
				g.base,
				g.item("big", "3.0.0"),
			),
		)
	case "/search/query":
		json200(
			fmt.Sprintf(
				`{"totalHits":1,"query":%q,"data":[{"@id":"%s/reg/lib.a/index.json","id":"lib.a","registration":"%[2]s/reg/lib.a/index.json",`+
					`"versions":[{"@id":"%[2]s/reg/lib.a/1.0.0.json","version":"1.0.0"}]}]}`,
				r.URL.RawQuery,
				g.base,
			),
		)
	default:
		http.NotFound(w, r)
	}
}

// nugetFixture serves the feed through a nuget proxy repository.
func nugetFixture(t *testing.T) (*fixture, *nugetFeed) {
	t.Helper()

	feed := &nugetFeed{t: t, nupkg: buildNupkg(t, "lib.a", "1.0.0")}
	f := newFixture(t, feed.handler)
	feed.base = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	feed.cdn = feed.base
	f.repo.Format = "nuget"
	f.repo.Name = "nugetorg"
	f.repo.UpstreamURL = feed.base + "/v3/index.json"
	f.svc.SetBaseURL(oaBase + "/")

	return f, feed
}

func jsonOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, w.Body.String())
	}

	return v
}

func TestNuGetProxy_ServiceIndexIsRewritten(t *testing.T) {
	f, _ := nugetFixture(t)

	doc := jsonOf(t, f.do(http.MethodGet, "v3/index.json"))

	got := map[string]string{}

	for _, r := range doc["resources"].([]any) {
		res := r.(map[string]any)
		got[res["@type"].(string)] = res["@id"].(string)
	}

	want := map[string]string{
		"RegistrationsBaseUrl/3.6.0": nugetRepoURL + "/v3/registration/",
		"PackageBaseAddress/3.0.0":   nugetRepoURL + "/v3/package/",
		"SearchQueryService":         nugetRepoURL + "/v3/query",
		"SearchAutocompleteService":  nugetRepoURL + "/v3/autocomplete",
	}

	if len(got) != len(want) {
		t.Errorf("resources = %v, want only %v (no publish, catalog, other registration versions)", got, want)
	}

	for typ, id := range want {
		if got[typ] != id {
			t.Errorf("%s = %q, want %q", typ, got[typ], id)
		}
	}

	f.do(http.MethodGet, "v3/index.json")

	if n := f.hits.Load(); n != 1 {
		t.Errorf("upstream requests = %d, want 1 (second index request is a cache hit)", n)
	}
}

func TestNuGetProxy_FlatContainerAndRestoreWithUpstreamDown(t *testing.T) {
	f, feed := nugetFixture(t)

	// What `dotnet restore` of lib.a 1.0.0 requests.
	versions := jsonOf(t, f.do(http.MethodGet, "v3/package/Lib.A/index.json"))
	if v := versions["versions"].([]any); len(v) != 1 || v[0] != "1.0.0" {
		t.Fatalf("versions = %v", v)
	}

	reg := f.do(http.MethodGet, "v3/registration/lib.a/index.json")
	if reg.Code != http.StatusOK {
		t.Fatalf("registration = %d", reg.Code)
	}

	for _, p := range []string{"v3/registration/lib.a/1.0.0.json", "v3/package/lib.a/1.0.0/lib.a.nuspec"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", p, w.Code, w.Body.String())
		}
	}

	nupkgPath := "v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"

	if w := f.do(http.MethodGet, nupkgPath); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), feed.nupkg) {
		t.Fatalf("nupkg = %d (%d bytes)", w.Code, w.Body.Len())
	}

	// The upstream goes away and the cached metadata expires.
	feed.down.Store(true)

	for _, key := range []string{"v3/index.json", "v3/package/lib.a/index.json", "v3/registration/lib.a/index.json"} {
		f.db.expire("repo-1", key)
	}

	for _, p := range []string{"v3/index.json", "v3/package/lib.a/index.json", "v3/registration/lib.a/index.json"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusOK || w.Header().Get("Warning") == "" {
			t.Errorf("expired %s with upstream down = %d, Warning %q; want 200 stale", p, w.Code, w.Header().Get("Warning"))
		}
	}

	// Package files are immutable: no revalidation, no Warning, upstream untouched.
	before := f.hits.Load()

	for _, p := range []string{nupkgPath, "v3/package/lib.a/1.0.0/lib.a.nuspec"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusOK || w.Header().Get("Warning") != "" {
			t.Errorf("cached %s = %d, Warning %q", p, w.Code, w.Header().Get("Warning"))
		}
	}

	if f.hits.Load() != before {
		t.Error("cached package files were revalidated against the upstream")
	}

	// Never requested before: nothing to serve.
	if w := f.do(http.MethodGet, "v3/package/other/index.json"); w.Code != http.StatusBadGateway {
		t.Errorf("uncached package with upstream down = %d, want 502", w.Code)
	}
}

func TestNuGetProxy_RegistrationIsDecompressedAndRewritten(t *testing.T) {
	f, feed := nugetFixture(t)

	w := f.do(http.MethodGet, "v3/registration/lib.a/index.json")
	doc := jsonOf(t, w)

	if doc["@id"] != nugetRepoURL+"/v3/registration/lib.a/index.json" {
		t.Errorf("@id = %v", doc["@id"])
	}

	page := doc["items"].([]any)[0].(map[string]any)
	if page["@id"] != nugetRepoURL+"/v3/registration/lib.a/index.json#page/1.0.0/1.0.0" || page["parent"] != doc["@id"] {
		t.Errorf("page = %v", page)
	}

	item := page["items"].([]any)[0].(map[string]any)
	wantContent := nugetRepoURL + "/v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"

	if item["@id"] != nugetRepoURL+"/v3/registration/lib.a/1.0.0.json" || item["packageContent"] != wantContent ||
		item["registration"] != doc["@id"] {
		t.Errorf("item = %v", item)
	}

	entry := item["catalogEntry"].(map[string]any)
	if entry["packageContent"] != wantContent {
		t.Errorf("catalogEntry.packageContent = %v", entry["packageContent"])
	}

	// Catalog URLs are not served here; text that merely mentions a URL is kept.
	if entry["@id"] != feed.base+"/catalog/lib.a.1.0.0.json" ||
		entry["description"] != "see https://api.nuget.org/v3-flatcontainer/ & <more>" {
		t.Errorf("catalogEntry = %v", entry)
	}

	if strings.Contains(w.Body.String(), feed.base+"/reg") || strings.Contains(w.Body.String(), feed.base+"/flat") {
		t.Errorf("upstream URL leaked: %s", w.Body.String())
	}

	leaf := jsonOf(t, f.do(http.MethodGet, "v3/registration/lib.a/1.0.0.json"))
	if leaf["packageContent"] != wantContent || leaf["registration"] != doc["@id"] || leaf["@id"] != item["@id"] {
		t.Errorf("leaf = %v", leaf)
	}

	// The cache keeps what arrived.
	e := f.db.entry("repo-1", "v3/registration/lib.a/index.json")
	if e == nil || e.SizeBytes == int64(w.Body.Len()) {
		t.Errorf("cache entry = %+v, want the upstream (gzip) bytes", e)
	}
}

func TestNuGetProxy_PagedRegistrationServesPagesThroughTheRepository(t *testing.T) {
	f, feed := nugetFixture(t)

	doc := jsonOf(t, f.do(http.MethodGet, "v3/registration/big/index.json"))
	pages := doc["items"].([]any)

	wantID := nugetRepoURL + "/v3/registration/big/page/1.0.0/2.0.0.json"
	if len(pages) != 2 || pages[0].(map[string]any)["@id"] != wantID ||
		pages[0].(map[string]any)["parent"] != nugetRepoURL+"/v3/registration/big/index.json" {
		t.Fatalf("pages = %v", pages)
	}

	page := jsonOf(t, f.do(http.MethodGet, "v3/registration/big/page/1.0.0/2.0.0.json"))
	if page["@id"] != wantID || len(page["items"].([]any)) != 2 {
		t.Fatalf("page = %v", page)
	}

	item := page["items"].([]any)[1].(map[string]any)
	if item["packageContent"] != nugetRepoURL+"/v3/package/big/2.0.0/big.2.0.0.nupkg" {
		t.Errorf("page item = %v", item)
	}

	// A page the index does not list is never fetched.
	if w := f.do(http.MethodGet, "v3/registration/big/page/9.0.0/9.9.9.json"); w.Code != http.StatusNotFound {
		t.Errorf("unlisted page = %d, want 404", w.Code)
	}

	if feed.seen("/reg/big/page/9.0.0/9.9.9.json") != 0 {
		t.Error("an arbitrary page path was fetched from upstream")
	}
}

func TestNuGetProxy_PackageDigestIsEnforced(t *testing.T) {
	nupkgPath := "v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"

	for name, tc := range map[string]struct {
		setup  func(*nugetFeed)
		wantOK bool
	}{
		"matching hash and size":        {func(*nugetFeed) {}, true},
		"hash of other bytes":           {func(g *nugetFeed) { g.hashOf = []byte("something else") }, false},
		"wrong size":                    {func(g *nugetFeed) { g.catalogSize = 7 }, false},
		"no hash or size, valid zip":    {func(g *nugetFeed) { g.noHash = true }, true},
		"catalog entry gone":            {func(g *nugetFeed) { g.noCatalog = true }, true},
		"no registration leaf":          {func(g *nugetFeed) { g.noLeaf = true }, true},
		"not a zip, no published hash":  {func(g *nugetFeed) { g.noCatalog, g.nupkg = true, []byte("not a zip at all") }, false},
		"zip without nuspec":            {func(g *nugetFeed) { g.noCatalog, g.nupkg = true, zipOf(t, "lib/a.dll", "x") }, false},
		"truncated zip, no catalog":     {func(g *nugetFeed) { g.noCatalog, g.nupkg = true, g.nupkg[:len(g.nupkg)/2] }, false},
		"replaced bytes with valid zip": {func(g *nugetFeed) { g.hashOf = buildNupkg(t, "lib.a", "9.9.9") }, false},
	} {
		t.Run(name, func(t *testing.T) {
			f, feed := nugetFixture(t)
			tc.setup(feed)

			w := f.do(http.MethodGet, nupkgPath)
			e := f.db.entry("repo-1", nupkgPath)

			if tc.wantOK {
				if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), feed.nupkg) || e == nil {
					t.Errorf("= %d (%d bytes), cache %+v; want the package served and cached", w.Code, w.Body.Len(), e)
				}

				return
			}

			if w.Code != http.StatusBadGateway || e != nil {
				t.Errorf("= %d, cache %+v; want 502 and nothing cached", w.Code, e)
			}

			if strings.Contains(w.Body.String(), "binary") || strings.Contains(w.Body.String(), "not a zip") {
				t.Error("refused content leaked into the response")
			}
		})
	}
}

func zipOf(t *testing.T, name, body string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.WriteString(w, body)

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestNuGetProxy_VersionSpellingsShareOneCacheEntry(t *testing.T) {
	f, feed := nugetFixture(t)

	for _, p := range []string{
		"v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg",
		"v3/package/LIB.A/1.0/LIB.A.1.0.nupkg",
		"v3/package/lib.a/1.0.0.0/lib.a.1.0.0.0.nupkg",
	} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d", p, w.Code)
		}
	}

	if n := feed.seen("/flat/lib.a/1.0.0/lib.a.1.0.0.nupkg"); n != 1 {
		t.Errorf("upstream nupkg requests = %d, want 1", n)
	}

	if w := f.do(http.MethodGet, "v3/package/lib.a/1.0.0/other.1.0.0.nupkg"); w.Code != http.StatusNotFound {
		t.Errorf("foreign file name = %d, want 404", w.Code)
	}
}

func TestNuGetProxy_CredentialsOnlyGoToTheConfiguredUpstream(t *testing.T) {
	f, feed := nugetFixture(t)
	f.repo.UpstreamUsername = "ci"

	// The flat container is advertised on a second server, as a CDN would be.
	cdn := &nugetFeed{t: t, nupkg: feed.nupkg}
	server := httptest.NewServer(http.HandlerFunc(cdn.handler))
	t.Cleanup(server.Close)

	cdn.base, cdn.cdn = server.URL, server.URL
	feed.cdn = server.URL

	if w := f.do(http.MethodGet, "v3/package/lib.a/1.0.0/lib.a.1.0.0.nupkg"); w.Code != http.StatusOK {
		t.Fatalf("nupkg from the CDN host = %d", w.Code)
	}

	for p, auths := range feed.auths {
		if auths[0] == "" {
			t.Errorf("request to the configured upstream %s carried no credentials", p)
		}
	}

	if len(cdn.auths["/flat/lib.a/1.0.0/lib.a.1.0.0.nupkg"]) != 1 || cdn.auths["/flat/lib.a/1.0.0/lib.a.1.0.0.nupkg"][0] != "" {
		t.Errorf("credentials sent to another origin: %v", cdn.auths)
	}
}

func TestNuGetProxy_SearchIsPassedThroughAndRewritten(t *testing.T) {
	f, feed := nugetFixture(t)

	w := f.do(http.MethodGet, "v3/query")
	if w.Code != http.StatusOK {
		t.Fatalf("search = %d", w.Code)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v3/query?q=lib&take=5", nil)
	w = httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, "v3/query")

	doc := jsonOf(t, w)
	if doc["query"] != "q=lib&take=5" {
		t.Errorf("query was not forwarded: %v", doc["query"])
	}

	hit := doc["data"].([]any)[0].(map[string]any)
	if hit["@id"] != nugetRepoURL+"/v3/registration/lib.a/index.json" || hit["registration"] != hit["@id"] ||
		hit["versions"].([]any)[0].(map[string]any)["@id"] != nugetRepoURL+"/v3/registration/lib.a/1.0.0.json" {
		t.Errorf("hit = %v", hit)
	}

	if strings.Contains(w.Body.String(), feed.base) {
		t.Errorf("upstream URL leaked: %s", w.Body.String())
	}

	// Uncached: every query reaches the upstream.
	if feed.seen("/search/query") != 2 {
		t.Errorf("search requests = %d, want 2", feed.seen("/search/query"))
	}
}

func TestNuGetProxy_RejectsWritesAndBadPaths(t *testing.T) {
	f, _ := nugetFixture(t)

	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequestWithContext(t.Context(), m, "/", nil)
		w := httptest.NewRecorder()
		f.svc.Serve(w, req, f.repo, "v3/package")

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, w.Code)
		}
	}

	for _, p := range []string{
		"", "v3", "v3/", "v2/index.json", "v3/package/index.json", "v3/package/../index.json",
		"v3/package/lib.a/index.json/x", "v3/package/lib.a/1.0.0/lib.a.1.0.0.zip", "v3/registration/..%2fx/index.json",
		"v3/registration/lib.a/page/1.0.0.json", "v3/registration/lib.a/1.0.0.txt", "v3/package/lib a/index.json",
	} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d, want 404", p, w.Code)
		}
	}

	if f.hits.Load() != 0 {
		t.Errorf("malformed paths reached the upstream (%d requests)", f.hits.Load())
	}
}

func TestNuGetProxy_UnusableUpstream(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"html answer":     func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>portal</html>") },
		"index not found": http.NotFound,
		"no usable resource": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"version":"3.0.0","resources":[]}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, handler)
			f.repo.Format = "nuget"
			f.svc.SetBaseURL(oaBase)

			for _, p := range []string{"v3/index.json", "v3/package/lib.a/index.json", "v3/registration/lib.a/index.json"} {
				if w := f.do(http.MethodGet, p); w.Code != http.StatusBadGateway {
					t.Errorf("GET %s = %d, want 502", p, w.Code)
				}
			}
		})
	}
}

func TestNuGetProxy_MetadataRevalidates(t *testing.T) {
	f, _ := nugetFixture(t)

	for _, p := range []string{"v3/index.json", "v3/package/lib.a/index.json", "v3/registration/lib.a/index.json"} {
		etag := f.do(http.MethodGet, p).Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", p)
		}

		if w := f.doWith(p, map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified {
			t.Errorf("%s revalidation = %d, want 304", p, w.Code)
		}
	}
}

func TestParseNuGetPath(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want nugetPath
		ok   bool
	}{
		{"v3/index.json", nugetPath{kind: nugetIndex}, true},
		{"/v3/query", nugetPath{kind: nugetQuery}, true},
		{"v3/autocomplete", nugetPath{kind: nugetAutocomplete}, true},
		{"v3/package/Newtonsoft.Json/index.json", nugetPath{kind: nugetVersions, id: "newtonsoft.json"}, true},
		{"v3/package/Newtonsoft.Json/13.0.1/Newtonsoft.Json.13.0.1.nupkg",
			nugetPath{kind: nugetPackageFile, id: "newtonsoft.json", version: "13.0.1", file: "newtonsoft.json.13.0.1.nupkg"}, true},
		{"v3/package/a/1.0/a.1.0.nupkg", nugetPath{kind: nugetPackageFile, id: "a", version: "1.0.0", file: "a.1.0.0.nupkg"}, true},
		{"v3/package/a/2.0.0-Beta/a.nuspec", nugetPath{kind: nugetPackageFile, id: "a", version: "2.0.0-beta", file: "a.nuspec"}, true},
		{"v3/registration/A/index.json", nugetPath{kind: nugetRegIndex, id: "a"}, true},
		{"v3/registration/A/1.0.0.json", nugetPath{kind: nugetRegLeaf, id: "a", version: "1.0.0"}, true},
		{"v3/registration/a/page/1.0.0/2.0.0-rc.1.json", nugetPath{kind: nugetRegPage, id: "a", lower: "1.0.0", upper: "2.0.0-rc.1"}, true},
		{"v3/package/../x/index.json", nugetPath{}, false},
		{"v3/package/a/../a.nuspec", nugetPath{}, false},
		{"v3/registration/a/page/../2.0.0.json", nugetPath{}, false},
		{"v3/registration/a/b/c/d/e.json", nugetPath{}, false},
		{"v3/other", nugetPath{}, false},
	} {
		got, ok := parseNuGetPath(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseNuGetPath(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNuGetImmutable(t *testing.T) {
	for key, want := range map[string]bool{
		"v3/package/a/1.0.0/a.1.0.0.nupkg":        true,
		"v3/package/a/1.0.0/a.nuspec":             true,
		"v3/package/a/index.json":                 false,
		"v3/registration/a/index.json":            false,
		"v3/registration/a/1.0.0.json":            false,
		"v3/index.json":                           false,
		"v3/registration/a/page/1.0.0/2.0.0.json": false,
	} {
		if got := Immutable("nuget", key); got != want {
			t.Errorf("Immutable(nuget, %q) = %v, want %v", key, got, want)
		}
	}
}

// A gzip document that inflates past the limit is the upstream's doing: 502,
// not a server error.
func TestNuGetProxy_OversizedGzipDocumentIsAnUpstreamError(t *testing.T) {
	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(make([]byte, maxNuGetDocBytes+1)); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := gunzipDoc(buf.Bytes())
	if !errors.Is(err, errUpstream) {
		t.Fatalf("gunzipDoc = %v, want errUpstream", err)
	}

	f := newFixture(t, http.NotFound)
	w := httptest.NewRecorder()
	f.svc.writeResolveError(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), err)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", w.Code)
	}
}
