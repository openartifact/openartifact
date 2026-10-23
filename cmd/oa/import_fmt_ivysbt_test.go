// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/format/base"
)

// ─── destination ──────────────────────────────────────────────────────────────

// ivySBTDest fronts a fakeOA with what the generic fake does not model of the
// ivy and sbt handlers: the package listing the real server serves (rows named
// by coordinates, not by repository path; see components.sql) and immutable
// releases (409 for different bytes at an existing address).
type ivySBTDest struct {
	*fakeOA

	front     *httptest.Server
	immutable atomic.Bool
	puts      atomic.Int32
}

func newIvySBTDest(t *testing.T, format string) *ivySBTDest {
	t.Helper()

	d := &ivySBTDest{fakeOA: newFakeOA(t, map[string]*fakeRepo{"dst": {format: format, repoType: "hosted"}})}
	d.front = httptest.NewServer(d.handler())
	t.Cleanup(d.front.Close)

	return d
}

func (d *ivySBTDest) handler() http.Handler {
	inner := d.srv.Config.Handler

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/packages"):
			serveIvySBTListing(inner, w, r)
		case r.Method == http.MethodPut:
			d.puts.Add(1)

			if d.admitPut(w, r) {
				inner.ServeHTTP(w, r)
			}
		default:
			inner.ServeHTTP(w, r)
		}
	})
}

// admitPut answers 409 as the server does when releases are immutable and the
// address holds other bytes; the body is restored for the fake handler.
func (d *ivySBTDest) admitPut(w http.ResponseWriter, r *http.Request) bool {
	data, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(data))

	if !d.immutable.Load() {
		return true
	}

	repoPath := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)[3]

	d.mu.Lock()
	it := d.find("dst", repoPath)
	d.mu.Unlock()

	if it != nil && it.sha != sum256(data) {
		http.Error(w, "released versions are immutable in this repository", http.StatusConflict)
		return false
	}

	return true
}

// serveIvySBTListing rewrites the fake's listing (file name = repository path)
// into the rows the real server lists for the format in the query.
func serveIvySBTListing(inner http.Handler, w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	inner.ServeHTTP(rec, r)

	var body struct {
		Total    int                 `json:"total"`
		Packages []map[string]string `json:"packages"`
	}

	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())

		return
	}

	format := r.URL.Query().Get("format")

	for _, p := range body.Packages {
		p["name"], p["version"], p["filename"] = ivySBTServerRow(format, p["filename"])
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// ivySBTServerRow is name, version and file name of the listing row of the file
// stored at repoPath, read with the server's own path parser.
func ivySBTServerRow(format, repoPath string) (string, string, string) {
	cur, _, _ := base.SplitArtifactPath(repoPath)
	filename := cur.Filename

	if format == "ivy" {
		filename += "." + strings.TrimPrefix(path.Ext(cur.Filename), ".")
	}

	return cur.Group + ":" + cur.Module, cur.Version, filename
}

// runImportVia runs an import from src into the fakeOA oa, reaching it through
// front (a proxy of the fake that injects faults or other server behaviour).
func runImportVia(t *testing.T, cfg importConfig, src *httptest.Server, oa *fakeOA, front *httptest.Server) *importReport {
	t.Helper()

	c := &Client{baseURL: front.URL, token: oa.token, httpClient: front.Client()}

	imp, err := newImporter(cfg, newImportSource(cfg, src.Client()), &importTarget{c: c}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(imp.state.close)

	return imp.run(context.Background())
}

func (d *ivySBTDest) runVia(t *testing.T, cfg importConfig, src *httptest.Server, front *httptest.Server) *importReport {
	t.Helper()

	return runImportVia(t, cfg, src, d.fakeOA, front)
}

func (d *ivySBTDest) run(t *testing.T, cfg importConfig, src *httptest.Server) *importReport {
	t.Helper()

	return d.runVia(t, cfg, src, d.front)
}

// ─── source ───────────────────────────────────────────────────────────────────

// ivySBTFixture is an Artifactory repository and which of its files are not
// payload.
type ivySBTFixture struct {
	format  string
	files   []fakeFile
	ignored []string
}

func (fx ivySBTFixture) payload() []fakeFile {
	return slices.DeleteFunc(slices.Clone(fx.files), func(f fakeFile) bool { return slices.Contains(fx.ignored, f.path) })
}

func artiFile(p, content string, props ...string) fakeFile {
	f := fakeFile{path: p, data: []byte(content), created: artiStamp}

	if len(props) > 0 {
		f.props = map[string][]string{props[0]: {props[1]}}
	}

	return f
}

func ivySBTFixtures() []ivySBTFixture {
	return []ivySBTFixture{
		{
			format: "ivy",
			files: []fakeFile{
				artiFile("org.acme/lib/1.0/jars/lib-1.0.jar", "ivy-jar", "build.number", "42"),
				artiFile("org.acme/lib/1.0/ivys/ivy-1.0.xml", "<ivy-module/>"),
				artiFile("org.acme/lib/1.0/srcs/lib-1.0-sources.jar", "ivy-sources"),
				artiFile("com/example/app/1.0/app-1.0.jar", "app-jar"),
				artiFile("com/example/app/1.0/app-1.0.pom", "<project/>"),
				artiFile("com/example/app/1.0/app-1.0.jar.asc", "signature"),
				artiFile("org.acme/lib/1.0/jars/lib-1.0.jar.sha1", "x"),
				artiFile("org.acme/lib/1.0/jars/lib-1.0.jar.md5", "x"),
				artiFile("com/example/app/1.0/app-1.0.pom.sha256", "x"),
				artiFile("com/example/app/maven-metadata.xml", "<metadata/>"),
				artiFile("lone/lib.jar", "no module, no version"),
			},
			ignored: []string{
				"org.acme/lib/1.0/jars/lib-1.0.jar.sha1", "org.acme/lib/1.0/jars/lib-1.0.jar.md5",
				"com/example/app/1.0/app-1.0.pom.sha256", "com/example/app/maven-metadata.xml", "lone/lib.jar",
			},
		},
		{
			format: "sbt",
			files: []fakeFile{
				artiFile("com/acme/lib_2.13/1.0/lib_2.13-1.0.jar", "sbt-jar", "build.number", "7"),
				artiFile("com/acme/lib_2.13/1.0/lib_2.13-1.0.pom", "<project/>"),
				artiFile("com/acme/lib_2.13/1.0/lib_2.13-1.0-sources.jar", "sbt-sources"),
				artiFile("org.acme/lib/scala_2.13/sbt_1.0/1.0/jars/lib.jar", "plugin-jar"),
				artiFile("org.acme/lib/scala_2.13/sbt_1.0/1.0/ivys/ivy.xml", "<ivy-module/>"),
				artiFile("com/acme/lib_2.13/1.0/lib_2.13-1.0.pom.sha1", "x"),
				artiFile("com/acme/lib_2.13/1.0/lib_2.13-1.0.jar.md5", "x"),
				artiFile("com/acme/lib_2.13/maven-metadata.xml", "<metadata/>"),
			},
			ignored: []string{
				"com/acme/lib_2.13/1.0/lib_2.13-1.0.pom.sha1", "com/acme/lib_2.13/1.0/lib_2.13-1.0.jar.md5",
				"com/acme/lib_2.13/maven-metadata.xml",
			},
		},
	}
}

// setup serves the fixture as Artifactory repository "src" and returns the
// import config mapping it to repository "dst" of a fresh destination.
func (fx ivySBTFixture) setup(t *testing.T, extra ...string) (importConfig, *httptest.Server, *ivySBTDest) {
	t.Helper()

	return fx.setupWith(t, fx.files, extra...)
}

func (fx ivySBTFixture) setupWith(t *testing.T, files []fakeFile, extra ...string) (importConfig, *httptest.Server, *ivySBTDest) {
	t.Helper()

	src := newFakeArtifactory(t, fakeSourceRepo{name: "src", format: fx.format, kind: "local", files: files})
	cfg := parseCfg(t, append([]string{"artifactory", "--source-url", src.URL, "--source-user", "svc", "--repo", "src:dst"}, extra...)...)
	cfg.SourceToken = "secret"

	return cfg, src, newIvySBTDest(t, fx.format)
}

// assertLanded checks that every payload file is stored under its source path
// with its bytes, creation time and copied properties, and nothing else is.
func (fx ivySBTFixture) assertLanded(t *testing.T, d *ivySBTDest) {
	t.Helper()

	for _, f := range fx.payload() {
		it := d.find("dst", f.path)
		if it == nil || it.sha != sum256(f.data) || it.created != artiWant {
			t.Errorf("%s: item = %+v", f.path, it)
			continue
		}

		for k, vs := range f.props {
			if it.props[k] != vs[0] {
				t.Errorf("%s: property %s = %q, want %q", f.path, k, it.props[k], vs[0])
			}
		}
	}

	if got := len(d.repos["dst"].items); got != len(fx.payload()) {
		t.Errorf("destination holds %d files, want %d", got, len(fx.payload()))
	}
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestImportIvySBTArtifactoryAQL(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t)

			rep := d.run(t, cfg, src)
			if rep.failed() || rep.Uploaded != len(fx.payload()) || rep.Ignored != len(fx.ignored) {
				t.Fatalf("report = %+v", rep)
			}

			fx.assertLanded(t, d)
		})
	}
}

func TestImportIvySBTArtifactoryStorageListing(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t, "--artifactory-listing", "storage")

			rep := d.run(t, cfg, src)
			if rep.failed() || rep.Uploaded != len(fx.payload()) || rep.Ignored != len(fx.ignored) {
				t.Fatalf("report = %+v", rep)
			}

			fx.assertLanded(t, d)
		})
	}
}

func TestImportIvySBTRerunIsIdempotent(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t)
			d.run(t, cfg, src)

			puts := d.puts.Load()
			rep := d.run(t, cfg, src)

			if rep.failed() || rep.Uploaded != 0 || rep.Present != len(fx.payload()) {
				t.Fatalf("second run = %+v, want everything present", rep)
			}

			if d.puts.Load() != puts {
				t.Fatalf("second run PUT %d files", d.puts.Load()-puts)
			}

			fx.assertLanded(t, d)
		})
	}
}

func TestImportIvySBTReplacesChangedContent(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t)
			target := fx.files[0]

			d.store("dst", target.path, []byte("older bytes"), "")

			rep := d.run(t, cfg, src)
			if rep.failed() || rep.Uploaded != len(fx.payload()) || rep.Present != 0 {
				t.Fatalf("report = %+v, want the stale copy replaced", rep)
			}

			fx.assertLanded(t, d)
		})
	}
}

func TestImportIvySBTResumesFromStateFile(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state.jsonl")
			cfg, src, d := fx.setup(t, "--state-file", state)

			if rep := d.run(t, cfg, src); rep.Uploaded != len(fx.payload()) {
				t.Fatalf("first run = %+v", rep)
			}

			// Only the state file knows the files are done.
			d.repos["dst"].items = nil
			puts := d.puts.Load()

			rep := d.run(t, cfg, src)
			if rep.Resumed != len(fx.payload()) || rep.Uploaded != 0 || d.puts.Load() != puts {
				t.Fatalf("resumed=%d uploaded=%d puts+=%d, want resume only", rep.Resumed, rep.Uploaded, d.puts.Load()-puts)
			}
		})
	}
}

func TestImportIvySBTDryRunWritesNothing(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t, "--dry-run")

			rep := d.run(t, cfg, src)
			if rep.failed() || !rep.DryRun || rep.Uploaded != len(fx.payload()) {
				t.Fatalf("report = %+v", rep)
			}

			if d.writes != 0 || d.puts.Load() != 0 || len(d.repos["dst"].items) != 0 {
				t.Fatalf("dry run wrote: writes=%d puts=%d", d.writes, d.puts.Load())
			}
		})
	}
}

func TestImportIvySBTChecksumMismatchRemovesTheCopy(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			bad := fx.files[0]
			bad.data, bad.sha256 = []byte("tampered"), sum256([]byte("original"))

			cfg, src, d := fx.setupWith(t, []fakeFile{bad})

			rep := d.run(t, cfg, src)
			if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "checksum mismatch") || rep.Failures[0].Path != bad.path {
				t.Fatalf("report = %+v", rep)
			}

			if d.find("dst", bad.path) != nil || d.deletes != 1 {
				t.Fatalf("bad copy was not removed (deletes=%d)", d.deletes)
			}
		})
	}
}

func TestImportIvySBTServerSideChecksumMismatchRemovesTheCopy(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t, "--path-prefix", fx.files[0].path)
			d.lieSHA = strings.Repeat("0", 64)

			rep := d.run(t, cfg, src)
			if rep.Failed != 1 || !strings.Contains(rep.Failures[0].Error, "server stored") {
				t.Fatalf("report = %+v", rep)
			}

			if len(d.repos["dst"].items) != 0 || d.deletes != 1 {
				t.Fatalf("corrupt copy left behind (deletes=%d)", d.deletes)
			}
		})
	}
}

func TestImportIvySBTImmutableReleaseConflictIsReported(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t, "--path-prefix", fx.files[0].path)
			d.store("dst", fx.files[0].path, []byte("released earlier"), "")
			d.immutable.Store(true)

			rep := d.run(t, cfg, src)

			var msg string
			if len(rep.Failures) == 1 {
				msg = rep.Failures[0].Error
			}

			if rep.Failed != 1 || !strings.Contains(msg, errUploadConflict.Error()) || !strings.Contains(msg, "immutable") {
				t.Fatalf("report = %+v", rep)
			}

			if it := d.find("dst", fx.files[0].path); it == nil || it.sha != sum256([]byte("released earlier")) {
				t.Fatalf("existing release was changed: %+v", it)
			}
		})
	}
}

func TestImportIvySBTRepeatsAFailedUpload(t *testing.T) {
	for _, fx := range ivySBTFixtures() {
		t.Run(fx.format, func(t *testing.T) {
			cfg, src, d := fx.setup(t, "--path-prefix", fx.files[0].path)

			flaky := newFlaky(t, d.handler(), func(r *http.Request) bool { return r.Method == http.MethodPut }, 1, failStatus(http.StatusServiceUnavailable))
			(&fakeSleeper{}).apply(cfg.retrier)

			rep := d.runVia(t, cfg, src, flaky.Server)
			if rep.failed() || rep.Uploaded != 1 || rep.Retries != 1 || flaky.hits() != 2 {
				t.Fatalf("report = %+v, upload attempts = %d", rep, flaky.hits())
			}

			if it := d.find("dst", fx.files[0].path); it == nil || it.sha != sum256(fx.files[0].data) {
				t.Fatalf("item = %+v", it)
			}
		})
	}
}

func TestImportIvySBTRegistration(t *testing.T) {
	for _, f := range []importFormat{formatIvy, formatSBT} {
		def := formatDefs[f]

		if len(def.nexus) != 0 || !slices.Equal(def.artifactory, []string{string(f)}) || !def.overwrites {
			t.Errorf("%s: nexus=%v artifactory=%v overwrites=%v", f, def.nexus, def.artifactory, def.overwrites)
		}
	}
}

func TestImportIvySBTImportable(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"org.acme/lib/1.0/jars/lib-1.0.jar", true},
		{"org.acme/lib/1.0/ivys/ivy-1.0.xml", true},
		{"org.acme/lib/1.0/lib.pom", true},
		{"com/example/app/1.0/app-1.0.jar", true},
		{"com/example/app/1.0/app-1.0.jar.asc", true},
		{"com/example/app/1.0-SNAPSHOT/app-1.0-20240101.120000-1.jar", true},
		{"org.acme/lib/1.0/jars/lib-1.0.jar.sha1", false},
		{"org.acme/lib/1.0/jars/lib-1.0.jar.md5", false},
		{"org.acme/lib/1.0/jars/lib-1.0.jar.sha256", false},
		{"org.acme/lib/1.0/jars/lib-1.0.jar.sha512", false},
		{"com/example/app/maven-metadata.xml", false},
		{"com/example/app/1.0-SNAPSHOT/maven-metadata.xml", false},
		{"org/lib/1.0", false},
		{"lone/lib.jar", false},
		{"lib.jar", false},
		{"org/lib/1.0//lib.jar", false},
		{"org/lib/1.0/", false},
	}

	for _, f := range []importFormat{formatIvy, formatSBT} {
		for _, tc := range cases {
			if got := importable(f, tc.path); got != tc.want {
				t.Errorf("importable(%s, %q) = %v, want %v", f, tc.path, got, tc.want)
			}
		}
	}
}

// TestSplitIvySBTPathAgreesWithServer keeps the importer's reading of a path in
// step with the one the server stores the file under; a difference would make
// every already-imported file look absent.
func TestSplitIvySBTPathAgreesWithServer(t *testing.T) {
	corpus := []string{
		"", "a", "a/b", "a/b/c", "a/b/c/d", "a//c/d", "a/b/c/", "/a/b/c/d",
		"org.acme/lib/1.0/jars/lib-1.0.jar",
		"org.acme/lib/1.0/ivys/ivy-1.0.xml",
		"com/example/lib/1.0/lib-1.0.jar",
		"com/example/lib/1.0/lib.jar",
		"com/example/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-1.jar",
		"com/example/lib/1.0-SNAPSHOT/lib-1.0-SNAPSHOT.jar",
		"com/acme/lib_2.13/1.0/lib_2.13-1.0-sources.jar",
		"org.acme/lib/scala_2.13/sbt_1.0/1.0/jars/lib.jar",
		"a/b/c/d/e/f/g/h",
		"x/y/1.0/y-1.0.jar",
		"x/y/z/y-z.jar",
	}

	for _, p := range corpus {
		want, _, wantOK := base.SplitArtifactPath(p)
		got, ok := splitIvySBTPath(p)

		if ok != wantOK || got.group != want.Group || got.module != want.Module || got.version != want.Version || got.filename != want.Filename {
			t.Errorf("splitIvySBTPath(%q) = %+v, %v; server reads %+v, %v", p, got, ok, want, wantOK)
		}
	}
}

// TestImportIvySBTPresentUsesListedCoordinates covers present() on listings
// as the server serves them: rows carry coordinates and a file name, and the
// ivy file name has the extension appended again.
func TestImportIvySBTPresentUsesListedCoordinates(t *testing.T) {
	shaA, shaB := sum256([]byte("a")), sum256([]byte("b"))

	ivyRows := []listedPackage{
		{ID: "r1", SHA256: shaA, Name: "org.acme:lib", Version: "1.0", Filename: "jars/lib-1.0.jar.jar"},
		{ID: "r2", SHA256: shaA, Name: "org.acme:other", Version: "1.0", Filename: "jars/lib-1.0.jar.jar"},
		{ID: "r3", SHA256: shaB, Name: "org.acme:lib", Version: "2.0", Filename: "jars/lib-1.0.jar.jar"},
		{ID: "r4", SHA256: shaA, Name: "com.acme:lib", Version: "1.0", Filename: "lib-1.0.jar.jar"},
		{ID: "r5", SHA256: shaA, Name: "no-module-separator", Version: "1.0", Filename: "x"},
	}
	sbtRows := []listedPackage{
		{ID: "s1", SHA256: shaA, Name: "com.acme:lib_2.13", Version: "1.0", Filename: "lib_2.13-1.0.jar"},
		{ID: "s2", SHA256: shaB, Name: "com.acme:lib_2.13", Version: "1.0", Filename: "lib_2.13-1.0.pom"},
	}

	cases := []struct {
		name   string
		format importFormat
		rows   []listedPackage
		path   string
		sha    string
		want   string // listing id, "" for absent
	}{
		{"ivy: same coordinates and digest", formatIvy, ivyRows, "org.acme/lib/1.0/jars/lib-1.0.jar", shaA, "r1"},
		{"ivy: same address, other digest is replaced", formatIvy, ivyRows, "org.acme/lib/1.0/jars/lib-1.0.jar", shaB, ""},
		{"ivy: identical bytes in another module do not count", formatIvy, ivyRows, "org.acme/third/1.0/jars/lib-1.0.jar", shaA, ""},
		{"ivy: another version", formatIvy, ivyRows, "org.acme/lib/2.0/jars/lib-1.0.jar", shaB, "r3"},
		{"ivy: maven layout", formatIvy, ivyRows, "com/acme/lib/1.0/lib-1.0.jar", shaA, "r4"},
		{"ivy: other file of the module", formatIvy, ivyRows, "org.acme/lib/1.0/ivys/ivy-1.0.xml", shaA, ""},
		{"ivy: the listing never shows the bare file name", formatIvy, []listedPackage{{ID: "x", SHA256: shaA, Name: "org.acme:lib", Version: "1.0", Filename: "jars/lib-1.0.jar"}}, "org.acme/lib/1.0/jars/lib-1.0.jar", shaA, ""},
		{"ivy: source without digest", formatIvy, ivyRows, "org.acme/lib/1.0/jars/lib-1.0.jar", "", ""},
		{"ivy: invalid path", formatIvy, ivyRows, "lib-1.0.jar", shaA, ""},
		{"sbt: jar", formatSBT, sbtRows, "com/acme/lib_2.13/1.0/lib_2.13-1.0.jar", shaA, "s1"},
		{"sbt: pom with its own digest", formatSBT, sbtRows, "com/acme/lib_2.13/1.0/lib_2.13-1.0.pom", shaB, "s2"},
		{"sbt: pom with the jar's digest", formatSBT, sbtRows, "com/acme/lib_2.13/1.0/lib_2.13-1.0.pom", shaA, ""},
		{"sbt: file not listed", formatSBT, sbtRows, "com/acme/lib_2.13/1.0/lib_2.13-1.0-sources.jar", shaA, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ix := &targetIndex{format: tc.format, all: tc.rows}

			e, ok := ix.lookup(sourceAsset{Path: tc.path, SHA256: tc.sha})
			if ok != (tc.want != "") || e.ID != tc.want {
				t.Fatalf("lookup = %+v, %v; want id %q", e, ok, tc.want)
			}
		})
	}
}

// TestImportIvySBTPresentFollowsTheListingItIsGiven: the lookup table is built
// per listing and must not leak from one repository's listing into the next.
func TestImportIvySBTPresentFollowsTheListingItIsGiven(t *testing.T) {
	sha := sum256([]byte("a"))
	asset := sourceAsset{Path: "org.acme/lib/1.0/jars/lib-1.0.jar", SHA256: sha}
	listed := &targetIndex{format: formatIvy, all: []listedPackage{
		{ID: "r1", SHA256: sha, Name: "org.acme:lib", Version: "1.0", Filename: "jars/lib-1.0.jar.jar"},
	}}
	empty := &targetIndex{format: formatIvy}

	for i, tc := range []struct {
		ix   *targetIndex
		want bool
	}{{listed, true}, {empty, false}, {listed, true}, {nil, false}} {
		if _, ok := tc.ix.lookup(asset); ok != tc.want {
			t.Fatalf("lookup %d = %v, want %v", i, ok, tc.want)
		}
	}
}
