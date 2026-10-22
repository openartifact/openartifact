// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/mod/sumdb"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/mod/sumdb/note"
)

const (
	goMod1     = "example.com/Mod" // case-encoded on the wire: example.com/!mod
	goEscMod1  = "example.com/!mod"
	goVersion1 = "v1.2.3"
)

func TestParseGoPath(t *testing.T) {
	cases := []struct {
		path    string
		ok      bool
		kind    goKind
		module  string
		version string
	}{
		{"example.com/!mod/@v/list", true, goList, goMod1, ""},
		{"example.com/!mod/@latest", true, goLatest, goMod1, ""},
		{"example.com/!mod/@v/v1.2.3.info", true, goInfo, goMod1, "v1.2.3"},
		{"example.com/!mod/@v/v1.2.3.mod", true, goMod, goMod1, "v1.2.3"},
		{"example.com/!mod/@v/v1.2.3.zip", true, goZip, goMod1, "v1.2.3"},
		{"example.com/!mod/@v/v1.2.3-pre.1.zip", true, goZip, goMod1, "v1.2.3-pre.1"},
		{"example.com/!mod/@v/v2.0.0+incompatible.mod", true, goMod, goMod1, "v2.0.0+incompatible"},
		{"example.com/!mod/@v/v0.0.0-20200101000000-abcdefabcdef.info", true, goInfo, goMod1, "v0.0.0-20200101000000-abcdefabcdef"},
		{"example.com/!mod/@v/master.info", true, goInfo, goMod1, "master"},
		// Not a module path or version in the case-encoded form.
		{"example.com/Mod/@v/list", false, 0, "", ""},
		{"example.com/!mod/@v/!v1.2.3.zip", false, 0, "", ""},
		// .mod and .zip only exist for canonical versions.
		{"example.com/!mod/@v/master.zip", false, 0, "", ""},
		{"example.com/!mod/@v/v1.2.mod", false, 0, "", ""},
		{"example.com/!mod/@v/.zip", false, 0, "", ""},
		{"example.com/!mod/@v/", false, 0, "", ""},
		{"example.com/!mod/@v/v1.2.3.txt", false, 0, "", ""},
		{"example.com/!mod/v1.2.3.zip", false, 0, "", ""},
		{"/@latest", false, 0, "", ""},
		{"sumdb/sum.golang.org/supported", false, 0, "", ""},
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got, ok := parseGoPath(c.path)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}

			if ok && (got.kind != c.kind || got.module != c.module || got.version != c.version || got.key != c.path) {
				t.Errorf("parsed %+v", got)
			}
		})
	}
}

func TestImmutable_Go(t *testing.T) {
	cases := map[string]bool{
		"example.com/!mod/@v/v1.2.3.zip":              true,
		"example.com/!mod/@v/v1.2.3.mod":              true,
		"example.com/!mod/@v/v1.2.3.info":             true,
		"example.com/!mod/@v/v1.2.3-rc.1.info":        true,
		"example.com/!mod/@v/v2.0.0+incompatible.zip": true,
		"example.com/!mod/@v/master.info":             false,
		"example.com/!mod/@v/v1.info":                 false,
		"example.com/!mod/@v/list":                    false,
		"example.com/!mod/@latest":                    false,
		"sumdb/sum.golang.org/latest":                 false,
		"example.com/!mod/@v/v1.2.3.zip.sha256":       false,
	}

	for key, want := range cases {
		if got := Immutable("go", key); got != want {
			t.Errorf("Immutable(go, %q) = %v, want %v", key, got, want)
		}
	}
}

func TestGoUpstreamURL(t *testing.T) {
	got, err := goUpstreamURL("https://proxy.example/prefix/", "example.com/!mod/@v/v1.2.3.zip")
	if err != nil {
		t.Fatal(err)
	}

	if want := "https://proxy.example/prefix/example.com/%21mod/@v/v1.2.3.zip"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

// goModuleZip builds a module zip the way the go command expects it.
func goModuleZip(t *testing.T, mod, version string, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}

	slices.Sort(names)

	for _, name := range names {
		w, err := zw.Create(mod + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := io.WriteString(w, files[name]); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func goTestModule(t *testing.T, mod, version, marker string) (string, []byte) {
	t.Helper()

	modFile := "module " + mod + "\n\ngo 1.21\n"

	return modFile, goModuleZip(t, mod, version, map[string]string{
		"go.mod": modFile,
		"a.go":   "package a\n\n// " + marker + "\n",
	})
}

// goSumLines returns the go.sum lines of one module version.
func goSumLines(t *testing.T, mod, version, modFile string, zipFile []byte) []byte {
	t.Helper()

	zipPath := filepath.Join(t.TempDir(), "m.zip")
	if err := os.WriteFile(zipPath, zipFile, 0o600); err != nil {
		t.Fatal(err)
	}

	zh, err := dirhash.HashZip(zipPath, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}

	mh, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(modFile)), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return fmt.Appendf(nil, "%s %s %s\n%s %s/go.mod %s\n", mod, version, zh, mod, version, mh)
}

// fakeGoUpstream is an upstream GOPROXY serving files by escaped path.
type fakeGoUpstream struct {
	mu    sync.Mutex
	files map[string][]byte
	paths []string
	down  atomic.Bool
}

func (g *fakeGoUpstream) handler(w http.ResponseWriter, r *http.Request) {
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}

	g.mu.Lock()
	g.paths = append(g.paths, r.URL.EscapedPath())
	body, ok := g.files[strings.TrimPrefix(r.URL.Path, "/")]
	g.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	switch {
	case strings.HasSuffix(r.URL.Path, ".zip"):
		w.Header().Set("Content-Type", "application/zip")
	case strings.HasSuffix(r.URL.Path, ".info"), strings.HasSuffix(r.URL.Path, "@latest"):
		w.Header().Set("Content-Type", "application/json")
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}

	_, _ = w.Write(body)
}

func (g *fakeGoUpstream) set(path string, body []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.files[path] = body
}

// goEnv is a go proxy repository in front of a fake upstream, verified against
// a real (in-memory) checksum database that knows the genuine modules.
type goEnv struct {
	*fixture

	up       *fakeGoUpstream
	sumdb    *httptest.Server
	sumdbReq atomic.Int32
	known    map[string][]byte // "mod@version" -> go.sum lines
	genuine  struct {
		mod string
		zip []byte
	}
}

func newGoEnv(t *testing.T) *goEnv {
	t.Helper()

	e := &goEnv{up: &fakeGoUpstream{files: map[string][]byte{}}, known: map[string][]byte{}}
	e.fixture = newFixture(t, e.up.handler)

	e.repo.Format = "go"
	e.repo.UpstreamURL = strings.TrimSuffix(e.repo.UpstreamURL, "/maven2/")

	skey, vkey, err := note.GenerateKey(rand.Reader, "sumdb.oa.test")
	if err != nil {
		t.Fatal(err)
	}

	srv := sumdb.NewServer(sumdb.NewTestServer(skey, func(path, vers string) ([]byte, error) {
		lines, ok := e.known[path+"@"+vers]
		if !ok {
			return nil, os.ErrNotExist
		}

		return lines, nil
	}))

	e.sumdb = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.sumdbReq.Add(1)
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(e.sumdb.Close)

	e.svc.sumdb = &sumDB{name: "sumdb.oa.test", vkey: vkey, baseURL: e.sumdb.URL, client: e.svc.client, log: e.svc.log}

	// The genuine example.com/Mod@v1.2.3 is published upstream and recorded.
	modFile, zipFile := goTestModule(t, goMod1, goVersion1, "genuine")
	e.genuine.mod, e.genuine.zip = modFile, zipFile
	e.known[goMod1+"@"+goVersion1] = goSumLines(t, goMod1, goVersion1, modFile, zipFile)

	base := goEscMod1 + "/@v/"
	e.up.set(base+"list", []byte("v1.0.0\n"+goVersion1+"\n"))
	e.up.set(base+goVersion1+".info", []byte(`{"Version":"`+goVersion1+`","Time":"2024-01-02T03:04:05Z"}`))
	e.up.set(base+goVersion1+".mod", []byte(modFile))
	e.up.set(base+goVersion1+".zip", zipFile)
	e.up.set(goEscMod1+"/@latest", []byte(`{"Version":"`+goVersion1+`","Time":"2024-01-02T03:04:05Z"}`))

	return e
}

func (e *goEnv) get(path string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, path)
}

func TestGoProxy_ServesAndCaches(t *testing.T) {
	e := newGoEnv(t)
	base := goEscMod1 + "/@v/"

	want := map[string]string{
		base + "list":               "v1.0.0\n" + goVersion1 + "\n",
		base + goVersion1 + ".info": `{"Version":"` + goVersion1 + `","Time":"2024-01-02T03:04:05Z"}`,
		base + goVersion1 + ".mod":  e.genuine.mod,
		base + goVersion1 + ".zip":  string(e.genuine.zip),
		goEscMod1 + "/@latest":      `{"Version":"` + goVersion1 + `","Time":"2024-01-02T03:04:05Z"}`,
	}

	for path, body := range want {
		if w := e.get(path); w.Code != http.StatusOK || w.Body.String() != body {
			t.Fatalf("GET %s: status %d, body %.60q", path, w.Code, w.Body.String())
		}
	}

	if got := len(e.up.paths); got != len(want) {
		t.Fatalf("upstream requests = %d (%v), want %d", got, e.up.paths, len(want))
	}

	// The module path went upstream case-encoded and %-escaped as the go
	// command sends it.
	if !slices.Contains(e.up.paths, "/example.com/%21mod/@v/"+goVersion1+".zip") {
		t.Errorf("upstream paths = %v", e.up.paths)
	}

	// Everything again, with the upstream down: from the cache.
	e.up.down.Store(true)

	for path, body := range want {
		if path == base+"list" || path == goEscMod1+"/@latest" {
			continue
		}

		if w := e.get(path); w.Code != http.StatusOK || w.Body.String() != body {
			t.Fatalf("cached GET %s with upstream down: status %d", path, w.Code)
		}
	}

	// Module files are immutable; list and @latest carry the repository TTL.
	for _, k := range []string{base + goVersion1 + ".zip", base + goVersion1 + ".mod", base + goVersion1 + ".info"} {
		if exp := e.db.entry("repo-1", k).ExpiresAt; time.Until(exp) < 365*24*time.Hour {
			t.Errorf("%s expires %v, want immutable", k, exp)
		}
	}

	if exp := e.db.entry("repo-1", base+"list").ExpiresAt; time.Until(exp) > time.Hour {
		t.Errorf("list expires %v, want the repository TTL", exp)
	}

	// An expired list is served stale while the upstream is down.
	e.db.expire("repo-1", base+"list")

	w := e.get(base + "list")
	if w.Code != http.StatusOK || w.Header().Get("Warning") == "" || w.Body.String() != "v1.0.0\n"+goVersion1+"\n" {
		t.Errorf("stale list: status %d warning %q", w.Code, w.Header().Get("Warning"))
	}

	// ...but a file never fetched is a 502.
	if w := e.get(goEscMod1 + "/@v/v1.0.0.info"); w.Code != http.StatusBadGateway {
		t.Errorf("uncached file with upstream down: status %d, want 502", w.Code)
	}
}

func TestGoProxy_RejectsWhatTheChecksumDatabaseDoesNotRecord(t *testing.T) {
	base := goEscMod1 + "/@v/" + goVersion1

	cases := []struct {
		name   string
		path   string
		tamper func(e *goEnv)
	}{
		{"zip with other contents", base + ".zip", func(e *goEnv) {
			_, evil := goTestModule(t, goMod1, goVersion1, "evil")
			e.up.set(base+".zip", evil)
		}},
		{"mod with other contents", base + ".mod", func(e *goEnv) {
			e.up.set(base+".mod", []byte("module "+goMod1+"\n\nrequire evil.example/x v1.0.0\n"))
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newGoEnv(t)
			c.tamper(e)

			if w := e.get(c.path); w.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502", w.Code)
			}

			if e.db.entry("repo-1", c.path) != nil {
				t.Error("a file that failed verification was cached")
			}
		})
	}
}

func TestGoProxy_ChecksumDatabaseUnreachableFailsClosed(t *testing.T) {
	e := newGoEnv(t)
	e.sumdb.Close()

	path := goEscMod1 + "/@v/" + goVersion1 + ".zip"

	if w := e.get(path); w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", w.Code)
	}

	if e.db.entry("repo-1", path) != nil {
		t.Error("an unverified file was cached")
	}
}

func TestGoProxy_UnknownToChecksumDatabaseIsVerifiedStructurally(t *testing.T) {
	const mod, ver = "private.example/lib", "v0.1.0"

	modFile, zipFile := goTestModule(t, mod, ver, "private")

	cases := []struct {
		name string
		zip  []byte
		want int
	}{
		{"well-formed", zipFile, http.StatusOK},
		{"entries outside the module prefix", goModuleZip(t, "other.example/lib", ver, map[string]string{"go.mod": "module other.example/lib\n"}), http.StatusBadGateway},
		{"not a zip", []byte("<html>captive portal</html>"), http.StatusBadGateway},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newGoEnv(t)
			e.up.set("private.example/lib/@v/"+ver+".zip", c.zip)
			e.up.set("private.example/lib/@v/"+ver+".mod", []byte(modFile))

			if w := e.get("private.example/lib/@v/" + ver + ".zip"); w.Code != c.want {
				t.Fatalf("zip: status %d, want %d", w.Code, c.want)
			}
		})
	}

	t.Run("mod without module line", func(t *testing.T) {
		e := newGoEnv(t)
		e.up.set("private.example/lib/@v/"+ver+".mod", []byte("go 1.21\n"))

		if w := e.get("private.example/lib/@v/" + ver + ".mod"); w.Code != http.StatusBadGateway {
			t.Fatalf("status %d, want 502", w.Code)
		}
	})
}

func TestGoProxy_ChecksumDatabaseOffVerifiesStructureOnly(t *testing.T) {
	e := newGoEnv(t)
	e.svc.sumdb = nil

	_, evil := goTestModule(t, goMod1, goVersion1, "evil")
	e.up.set(goEscMod1+"/@v/"+goVersion1+".zip", evil)

	if w := e.get(goEscMod1 + "/@v/" + goVersion1 + ".zip"); w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 without a checksum database", w.Code)
	}

	if e.sumdbReq.Load() != 0 {
		t.Error("checksum database was asked although it is off")
	}

	e.up.set(goEscMod1+"/@v/"+goVersion1+".mod", []byte("not a go.mod"))

	if w := e.get(goEscMod1 + "/@v/" + goVersion1 + ".mod"); w.Code != http.StatusBadGateway {
		t.Fatalf("mod without module line: status %d, want 502", w.Code)
	}
}

func TestGoProxy_RefusesBodiesThatAreNotWhatTheyClaim(t *testing.T) {
	cases := []struct{ name, path, body string }{
		{"list of html", goEscMod1 + "/@v/list", "<html>blocked</html>"},
		{"info of another version", goEscMod1 + "/@v/" + goVersion1 + ".info", `{"Version":"v9.9.9"}`},
		{"info that is not json", goEscMod1 + "/@v/" + goVersion1 + ".info", "oops"},
		{"latest without version", goEscMod1 + "/@latest", `{}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newGoEnv(t)
			e.up.set(c.path, []byte(c.body))

			if w := e.get(c.path); w.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502", w.Code)
			}

			if e.db.entry("repo-1", c.path) != nil {
				t.Error("refused body was cached")
			}
		})
	}
}

func TestGoProxy_EmptyListIsAnAnswer(t *testing.T) {
	e := newGoEnv(t)
	e.up.set(goEscMod1+"/@v/list", nil)

	w := e.get(goEscMod1 + "/@v/list")
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
}

func TestGoProxy_QueryInfoUsesTTLAndNonCanonicalZipIsNotFound(t *testing.T) {
	e := newGoEnv(t)
	e.up.set(goEscMod1+"/@v/master.info", []byte(`{"Version":"v1.2.4-0.20240101000000-abcdefabcdef","Time":"2024-01-02T03:04:05Z"}`))

	if w := e.get(goEscMod1 + "/@v/master.info"); w.Code != http.StatusOK {
		t.Fatalf("master.info: status %d", w.Code)
	}

	if exp := e.db.entry("repo-1", goEscMod1+"/@v/master.info").ExpiresAt; time.Until(exp) > time.Hour {
		t.Errorf("a branch query expires %v, want the repository TTL", exp)
	}

	before := len(e.up.paths)

	if w := e.get(goEscMod1 + "/@v/master.zip"); w.Code != http.StatusNotFound {
		t.Errorf("master.zip: status %d, want 404", w.Code)
	}

	if len(e.up.paths) != before {
		t.Error("a non-canonical zip was requested upstream")
	}
}

func TestGoProxy_UnknownAndWrites(t *testing.T) {
	e := newGoEnv(t)

	for _, p := range []string{"example.com/!mod/@v/v7.7.7.zip", "other.example/x/@v/list", "sumdb/sum.golang.org/supported", "example.com/Mod/@v/list"} {
		if w := e.get(p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, w.Code)
		}
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/", strings.NewReader("zip"))
	w := httptest.NewRecorder()
	e.svc.Serve(w, req, e.repo, goEscMod1+"/@v/"+goVersion1+".zip")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: status %d, want 405", w.Code)
	}
}

func TestGoProxy_UpstreamCredentialsGoToTheUpstream(t *testing.T) {
	e := newGoEnv(t)
	e.repo.UpstreamUsername = "ci"

	var auth atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, "v1.0.0\n")
	}))
	t.Cleanup(srv.Close)

	e.repo.UpstreamURL = srv.URL

	if w := e.get(goEscMod1 + "/@v/list"); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	if got, _ := auth.Load().(string); !strings.HasPrefix(got, "Basic ") {
		t.Errorf("Authorization = %q, want Basic credentials", got)
	}
}

func TestNewSumDB(t *testing.T) {
	log := newGoEnv(t).svc.log

	if newSumDB("off", nil, log) != nil {
		t.Error("GOSUMDB=off must disable lookups")
	}

	cases := []struct {
		spec, name, url string
	}{
		{"", "sum.golang.org", "https://sum.golang.org"},
		{"sum.golang.org", "sum.golang.org", "https://sum.golang.org"},
		{"sum.golang.google.cn", "sum.golang.org", "https://sum.golang.google.cn"},
		{"db.example+deadbeef+AAAA", "db.example", "https://db.example"},
		{"db.example+deadbeef+AAAA https://mirror.example/sumdb/", "db.example", "https://mirror.example/sumdb"},
		{"db.example+deadbeef+AAAA mirror.example", "db.example", "https://mirror.example"},
		{"not-a-key", "sum.golang.org", "https://sum.golang.org"}, // invalid: default, never silently off
	}

	for _, c := range cases {
		d := newSumDB(c.spec, nil, log)
		if d == nil || d.name != c.name || d.baseURL != c.url {
			t.Errorf("newSumDB(%q) = %+v, want %s %s", c.spec, d, c.name, c.url)
		}
	}
}
