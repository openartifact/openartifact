// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	pkghelm "github.com/openartifact/openartifact/pkg/helm"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeChartRepo is an upstream classic chart repository served below
// /charts/: one chart with a relative URL, one whose URL points at a CDN
// origin, one that only lists an oci:// URL, plus digest edge cases.
type fakeChartRepo struct {
	mu sync.Mutex

	cdn   string // base URL of the second origin the absolute chart lives on
	files map[string][]byte
	// digests overrides the digest advertised for a chart file.
	digests map[string]string
	index   string // when set, served instead of the generated index
	down    atomic.Bool
	paths   []string
	auths   []string // Authorization headers seen by the repository origin
}

func newFakeChartRepo() *fakeChartRepo {
	return &fakeChartRepo{
		files: map[string][]byte{
			"app-1.0.0.tgz":      []byte("app chart bytes"),
			"app-1.0.0.tgz.prov": []byte("app provenance"),
			"nodigest-1.0.0.tgz": []byte("nodigest chart bytes"),
			"bad-1.0.0.tgz":      []byte("bad chart bytes"),
			"weird-1.0.0.tgz":    []byte("weird chart bytes"),
			"abs-2.0.0.tgz":      []byte("absolute chart bytes"),
		},
		digests: map[string]string{},
	}
}

func (g *fakeChartRepo) digestOf(file string) string {
	if d, ok := g.digests[file]; ok {
		return d
	}

	return sha256Hex(g.files[file])
}

func (g *fakeChartRepo) indexYAML() string {
	if g.index != "" {
		return g.index
	}

	return fmt.Sprintf(`apiVersion: v1
generated: "2026-01-01T00:00:00Z"
entries:
  app:
  - apiVersion: v2
    name: app
    version: 1.0.0
    appVersion: "9.9"
    description: The app
    annotations:
      category: Test
    dependencies:
    - name: sub
      repository: oci://registry.example/charts
      version: 1.x.x
    digest: %[1]s
    urls:
    - app-1.0.0.tgz
  abs:
  - name: abs
    version: 2.0.0
    digest: %[2]s
    urls:
    - %[3]s/dl/abs-2.0.0.tgz
  oci:
  - name: oci
    version: 1.0.0
    urls:
    - oci://registry.example/charts/oci:1.0.0
  nodigest:
  - name: nodigest
    version: 1.0.0
    urls:
    - nodigest-1.0.0.tgz
  bad:
  - name: bad
    version: 1.0.0
    digest: %[4]s
    urls:
    - bad-1.0.0.tgz
  weird:
  - name: weird
    version: 1.0.0
    digest: %[5]s
    urls:
    - weird-1.0.0.tgz
`, g.digestOf("app-1.0.0.tgz"), g.digestOf("abs-2.0.0.tgz"), g.cdn, g.digestOf("bad-1.0.0.tgz"), g.digestOf("weird-1.0.0.tgz"))
}

func (g *fakeChartRepo) handler(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	g.mu.Lock()
	g.paths = append(g.paths, r.URL.Path)
	g.auths = append(g.auths, r.Header.Get("Authorization"))
	g.mu.Unlock()

	file, ok := strings.CutPrefix(r.URL.Path, "/charts/")
	if !ok {
		http.NotFound(w, r)
		return
	}

	if file == "index.yaml" {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = io.WriteString(w, g.indexYAML())

		return
	}

	if b, ok := g.files[file]; ok && file != "abs-2.0.0.tgz" {
		_, _ = w.Write(b)
		return
	}

	http.NotFound(w, r)
}

func (g *fakeChartRepo) saw(p string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Contains(g.paths, p)
}

// helmFixture serves the fake repository through a helm proxy repository.
func helmFixture(t *testing.T) (*fixture, *fakeChartRepo) {
	t.Helper()

	g := newFakeChartRepo()
	f := newFixture(t, g.handler)

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.paths = append(g.paths, "cdn"+r.URL.Path)
		g.auths = append(g.auths, "cdn:"+r.Header.Get("Authorization"))
		g.mu.Unlock()

		if r.URL.Path == "/dl/abs-2.0.0.tgz" {
			_, _ = w.Write(g.files["abs-2.0.0.tgz"])
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(cdn.Close)

	g.cdn = cdn.URL
	f.repo.Format = "helm"
	f.repo.Name = "charts"
	f.repo.UpstreamURL = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/") + "/charts"
	return f, g
}

func parseIndexBody(t *testing.T, body string) map[string]pkghelm.ChartVersion {
	t.Helper()

	ix, err := pkghelm.ParseIndex([]byte(body))
	if err != nil {
		t.Fatalf("response is not a helm index: %v\n%s", err, body)
	}

	out := map[string]pkghelm.ChartVersion{}
	for cv := range ix.Versions() {
		out[cv.Name] = cv
	}

	return out
}

func TestHelmProxy_IndexURLsAreRewrittenAndCached(t *testing.T) {
	f, _ := helmFixture(t)

	for range 2 {
		w := f.do(http.MethodGet, "index.yaml")
		if w.Code != http.StatusOK {
			t.Fatalf("index.yaml = %d: %s", w.Code, w.Body.String())
		}

		got := parseIndexBody(t, w.Body.String())

		want := map[string]string{
			"app":      "charts/app-1.0.0.tgz",
			"abs":      "charts/abs-2.0.0.tgz",
			"nodigest": "charts/nodigest-1.0.0.tgz",
		}

		for name, u := range want {
			if cv, ok := got[name]; !ok || len(cv.URLs) != 1 || cv.URLs[0] != u {
				t.Errorf("%s = %+v, want the single url %s", name, got[name], u)
			}
		}

		if _, ok := got["oci"]; ok {
			t.Error("a chart that only lists an oci:// url must not be advertised")
		}

		if got["app"].Digest != sha256Hex([]byte("app chart bytes")) {
			t.Errorf("digest was not preserved: %q", got["app"].Digest)
		}

		for _, field := range []string{"category: Test", "appVersion:", "oci://registry.example/charts", "description: The app"} {
			if !strings.Contains(w.Body.String(), field) {
				t.Errorf("index lost %q:\n%s", field, w.Body.String())
			}
		}
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1 (second index is a cache hit)", got)
	}
}

func TestHelmProxy_IndexRevalidatesWithETag(t *testing.T) {
	f, _ := helmFixture(t)

	first := f.do(http.MethodGet, "index.yaml")
	etag := first.Header().Get("ETag")

	if etag == "" {
		t.Fatal("index.yaml carries no ETag")
	}

	w := f.doWith("index.yaml", map[string]string{"If-None-Match": etag})
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Errorf("conditional index.yaml = %d with %d body bytes, want 304", w.Code, w.Body.Len())
	}
}

func TestHelmProxy_ChartsAreVerifiedCachedAndServedWithUpstreamDown(t *testing.T) {
	f, g := helmFixture(t)

	for _, tc := range []struct{ path, want string }{
		{"charts/app-1.0.0.tgz", "app chart bytes"},
		{"charts/abs-2.0.0.tgz", "absolute chart bytes"},
		{"charts/nodigest-1.0.0.tgz", "nodigest chart bytes"},
	} {
		if w := f.do(http.MethodGet, tc.path); w.Code != http.StatusOK || w.Body.String() != tc.want {
			t.Fatalf("GET %s = %d %q", tc.path, w.Code, w.Body.String())
		}
	}

	if !g.saw("/charts/app-1.0.0.tgz") || !g.saw("cdn/dl/abs-2.0.0.tgz") {
		t.Errorf("charts were not fetched from the urls the index lists: %v", g.paths)
	}

	// The upstream goes away: charts are immutable and keep being served, the
	// expired index is served stale within max_stale_seconds.
	f.db.expire("repo-1", "index.yaml")
	g.down.Store(true)

	if w := f.do(http.MethodGet, "charts/app-1.0.0.tgz"); w.Code != http.StatusOK || w.Body.String() != "app chart bytes" {
		t.Errorf("cached chart with upstream down = %d %q", w.Code, w.Body.String())
	}

	w := f.do(http.MethodGet, "index.yaml")
	if w.Code != http.StatusOK || w.Header().Get("Warning") == "" {
		t.Fatalf("stale index = %d, Warning %q; want 200 with a stale warning", w.Code, w.Header().Get("Warning"))
	}

	if cv := parseIndexBody(t, w.Body.String())["app"]; len(cv.URLs) != 1 || cv.URLs[0] != "charts/app-1.0.0.tgz" {
		t.Errorf("stale index was not rewritten: %+v", cv)
	}

	if w := f.do(http.MethodGet, "charts/bad-1.0.0.tgz"); w.Code != http.StatusBadGateway {
		t.Errorf("uncached chart with upstream down = %d, want 502", w.Code)
	}
}

func TestHelmProxy_DigestMismatchIsRefusedAndNotCached(t *testing.T) {
	for name, digest := range map[string]string{
		"other bytes": sha256Hex([]byte("something else")),
		"not sha256":  "abc123",
		"wrong algo":  strings.Repeat("g", 64),
	} {
		t.Run(name, func(t *testing.T) {
			f, g := helmFixture(t)
			g.digests["bad-1.0.0.tgz"] = digest

			w := f.do(http.MethodGet, "charts/bad-1.0.0.tgz")
			if w.Code != http.StatusBadGateway {
				t.Fatalf("chart with a bad digest = %d, want 502", w.Code)
			}

			if strings.Contains(w.Body.String(), "chart bytes") {
				t.Error("refused chart content leaked into the response")
			}

			if e := f.db.entry("repo-1", "charts/bad-1.0.0.tgz"); e != nil {
				t.Errorf("chart with a bad digest was cached: %+v", e)
			}
		})
	}
}

func TestHelmProxy_DigestMayCarryAlgorithmPrefixAndUppercase(t *testing.T) {
	f, g := helmFixture(t)
	g.digests["app-1.0.0.tgz"] = "sha256:" + strings.ToUpper(sha256Hex([]byte("app chart bytes")))

	if w := f.do(http.MethodGet, "charts/app-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Errorf("chart = %d, want 200", w.Code)
	}
}

func TestHelmProxy_ChartNotListedInIndexIsNotFetched(t *testing.T) {
	f, g := helmFixture(t)

	for _, p := range []string{"charts/ghost-1.0.0.tgz", "charts/index.yaml", "charts/oci-1.0.0.tgz"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}

	for _, p := range []string{"/charts/ghost-1.0.0.tgz", "/charts/oci-1.0.0.tgz"} {
		if g.saw(p) {
			t.Errorf("arbitrary path %s was fetched from upstream", p)
		}
	}

	if e := f.db.entry("repo-1", "charts/ghost-1.0.0.tgz"); e != nil {
		t.Errorf("a negative entry was recorded for an unlisted chart: %+v", e)
	}
}

func TestHelmProxy_ProvenanceFileComesFromNextToTheChart(t *testing.T) {
	f, g := helmFixture(t)

	w := f.do(http.MethodGet, "charts/app-1.0.0.tgz.prov")
	if w.Code != http.StatusOK || w.Body.String() != "app provenance" {
		t.Fatalf("prov = %d %q", w.Code, w.Body.String())
	}

	if !g.saw("/charts/app-1.0.0.tgz.prov") {
		t.Errorf("provenance was not fetched next to the chart: %v", g.paths)
	}

	if Immutable("helm", "charts/app-1.0.0.tgz.prov") || !Immutable("helm", "charts/app-1.0.0.tgz") || Immutable("helm", "index.yaml") {
		t.Error("only chart archives are immutable")
	}

	if w := f.do(http.MethodGet, "charts/nodigest-1.0.0.tgz.prov"); w.Code != http.StatusNotFound {
		t.Errorf("missing prov = %d, want 404", w.Code)
	}
}

func TestHelmProxy_NonIndexUpstreamAnswerIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"html":      "<html><body>Maintenance</body></html>",
		"scalar":    "just text",
		"no apiver": "entries: {}\n",
		"truncated": "apiVersion: v1\nentries:\n  app: [",
		"aliases":   "apiVersion: v1\nentries:\n  app: &a []\n  other: *a\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, g := helmFixture(t)
			g.index = body

			if w := f.do(http.MethodGet, "index.yaml"); w.Code != http.StatusBadGateway {
				t.Errorf("index.yaml = %d, want 502", w.Code)
			}

			if e := f.db.entry("repo-1", "index.yaml"); e != nil {
				t.Errorf("an unusable index was cached: %+v", e)
			}
		})
	}
}

func TestHelmProxy_EmptyIndexIsServed(t *testing.T) {
	f, g := helmFixture(t)
	g.index = "apiVersion: v1\nentries: {}\n"

	w := f.do(http.MethodGet, "index.yaml")
	if w.Code != http.StatusOK || len(parseIndexBody(t, w.Body.String())) != 0 {
		t.Errorf("empty index = %d %q", w.Code, w.Body.String())
	}
}

func TestHelmProxy_CredentialsOnlyGoToTheUpstreamOrigin(t *testing.T) {
	f, g := helmFixture(t)
	f.repo.UpstreamUsername = "user"

	f.do(http.MethodGet, "index.yaml")

	if w := f.do(http.MethodGet, "charts/app-1.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("chart = %d", w.Code)
	}

	if w := f.do(http.MethodGet, "charts/abs-2.0.0.tgz"); w.Code != http.StatusOK {
		t.Fatalf("absolute chart = %d", w.Code)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	for _, a := range g.auths {
		switch {
		case strings.HasPrefix(a, "cdn:"):
			if a != "cdn:" {
				t.Errorf("credentials were sent to the CDN origin: %q", a)
			}
		case !strings.HasPrefix(a, "Basic "):
			t.Errorf("upstream request without credentials: %q", a)
		}
	}
}

func TestHelmProxy_RejectsOtherPathsAndWrites(t *testing.T) {
	f, _ := helmFixture(t)

	for _, p := range []string{"", "charts", "charts/", "charts/a/b-1.0.0.tgz", "charts/../index.yaml", "other.yaml", "index.yaml/x"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d, want 404", p, w.Code)
		}
	}

	if w := f.do(http.MethodPost, "charts"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST charts = %d, want 405", w.Code)
	}

	if f.hits.Load() != 0 {
		t.Errorf("rejected paths reached the upstream %d times", f.hits.Load())
	}
}

// Two entries whose URLs end in the same file name (different directories) are
// two different charts: the index must hand out two different paths, and each
// must serve its own bytes.
func TestHelmProxy_SameBasenameDifferentURLsStayDistinct(t *testing.T) {
	f, g := helmFixture(t)

	g.files["stable/foo-1.0.0.tgz"] = []byte("stable foo")
	g.files["incubator/foo-1.0.0.tgz"] = []byte("incubator foo")
	g.index = fmt.Sprintf(`apiVersion: v1
entries:
  foo:
  - name: foo
    version: 1.0.0
    digest: %s
    urls:
    - stable/foo-1.0.0.tgz
  foo-inc:
  - name: foo-inc
    version: 1.0.0
    digest: %s
    urls:
    - incubator/foo-1.0.0.tgz
`, sha256Hex(g.files["stable/foo-1.0.0.tgz"]), sha256Hex(g.files["incubator/foo-1.0.0.tgz"]))

	for range 2 { // the assignment is the same on every render
		idx := parseIndexBody(t, f.do(http.MethodGet, "index.yaml").Body.String())

		first, second := idx["foo"], idx["foo-inc"]
		if len(first.URLs) != 1 || len(second.URLs) != 1 {
			t.Fatalf("index entries lost their URLs: %+v %+v", first, second)
		}

		if first.URLs[0] == second.URLs[0] {
			t.Fatalf("both charts are served under %s", first.URLs[0])
		}

		if want := "charts/foo-1.0.0.tgz"; first.URLs[0] != want {
			t.Errorf("first claimant = %s, want the plain name %s", first.URLs[0], want)
		}

		for url, want := range map[string]string{first.URLs[0]: "stable foo", second.URLs[0]: "incubator foo"} {
			p := strings.TrimPrefix(url, oaBase+"/repos/charts/")

			if w := f.do(http.MethodGet, p); w.Code != http.StatusOK || w.Body.String() != want {
				t.Errorf("GET %s = %d %q, want %q", p, w.Code, w.Body.String(), want)
			}
		}
	}
}

// The rewritten URLs are relative, so neither the body nor the ETag of the
// proxied index depends on the origin the request came in on.
func TestHelmProxy_IndexIsIndependentOfOrigin(t *testing.T) {
	f, _ := helmFixture(t)

	get := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/charts/index.yaml", http.NoBody)
		req.Host = host

		w := httptest.NewRecorder()
		f.svc.Serve(w, req, f.repo, "index.yaml")

		return w
	}

	a, b := get("a.example"), get("b.example")
	if a.Code != http.StatusOK || a.Header().Get("ETag") == "" || a.Header().Get("ETag") != b.Header().Get("ETag") {
		t.Errorf("ETags %q and %q (status %d) differ across origins", a.Header().Get("ETag"), b.Header().Get("ETag"), a.Code)
	}

	if a.Body.String() != b.Body.String() || strings.Contains(a.Body.String(), "a.example") {
		t.Errorf("index differs across origins or embeds one:\n%s", a.Body.String())
	}
}
