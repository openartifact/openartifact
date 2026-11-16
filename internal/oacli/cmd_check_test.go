// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/pkg/lockfile"
)

// checkServer answers POST /api/v1/repositories/{repo}/availability from the
// package names: held/* is held, missing/* not found, unknown/* unknown,
// anything else available. It records the package names of every request.
type checkServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests [][]string
	// gets records the metadata GETs (--fetch) in arrival order, and bodies the
	// raw availability request bodies; a GET of missing/* answers 404.
	gets   []string
	bodies []string
	status int // non-zero: answer every request with this status and an error; set before serving
}

// checkRepos are the repositories the check server knows.
var checkRepos = []string{"php", "npm", "pypi", "crates", "gems", "nuget"} //nolint:gochecknoglobals // test fixture

func newCheckServer(t *testing.T, status int) *checkServer {
	t.Helper()

	s := &checkServer{status: status}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)

	return s
}

func (s *checkServer) serve(w http.ResponseWriter, r *http.Request) {
	if s.status != 0 {
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(`{"error":"forbidden","message":"no read access to php"}`))

		return
	}

	if r.Method == http.MethodGet && slices.ContainsFunc(checkRepos, func(repo string) bool {
		return strings.HasPrefix(r.URL.EscapedPath(), "/repos/"+repo+"/")
	}) {
		s.mu.Lock()
		s.gets = append(s.gets, r.URL.EscapedPath())
		s.mu.Unlock()

		if strings.Contains(r.URL.Path, "missing/") {
			http.NotFound(w, r)
			return
		}

		_, _ = w.Write([]byte(`{"packages":{}}`))

		return
	}

	repo, ok := strings.CutPrefix(r.URL.Path, "/api/v1/repositories/")
	if r.Method != http.MethodPost || !ok || !slices.Contains(checkRepos, strings.TrimSuffix(repo, "/availability")) ||
		!strings.HasSuffix(repo, "/availability") {
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		return
	}

	var body struct {
		Packages []struct{ Package, Version string } `json:"packages"`
	}

	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(raw))
	s.mu.Unlock()

	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	names := make([]string, len(body.Packages))
	results := make([]map[string]any, len(body.Packages))

	for i, p := range body.Packages {
		names[i] = p.Package
		res := map[string]any{"package": p.Package, "version": p.Version, "status": "available", "repository": "php-proxy",
			"available": true, "reasons": []any{}, "min_age": "off"}

		switch strings.SplitN(p.Package, "/", 2)[0] {
		case "held":
			res["status"], res["available"] = "held", false
			res["reasons"] = []any{map[string]any{"source": "curation", "detail": "blocked by repository curation: EOL, use 7.x"}}
		case "missing":
			res["status"], res["available"], res["repository"] = "not_found", false, "php"
			res["reasons"] = []any{map[string]any{"source": "not_found", "detail": "no member of the group has this version"}}
		case "weird":
			res["status"], res["available"] = "embargoed", false
		case "unknown":
			res["status"], res["available"] = "unknown", false
			res["reasons"] = []any{map[string]any{"source": "unknown", "detail": "the proxy has not cached the metadata"}}
		}

		results[i] = res
	}

	if len(names) > 0 && strings.HasPrefix(names[0], "short/") {
		results = results[:len(results)-1] // a malformed answer: one result missing
	}

	s.mu.Lock()
	s.requests = append(s.requests, names)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"repository": "php", "results": results})
}

// composerLock writes a composer.lock of the given packages (version 1.0.0)
// and dev packages, and returns its path.
func composerLock(t *testing.T, pkgs, dev []string) string {
	t.Helper()

	entries := func(names []string) []map[string]any {
		out := make([]map[string]any, len(names))
		for i, n := range names {
			out[i] = map[string]any{"name": n, "version": "1.0.0", "dist": map[string]any{"type": "zip"}}
		}

		return out
	}

	data, err := json.Marshal(map[string]any{"packages": entries(pkgs), "packages-dev": entries(dev)})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "composer.lock")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestCheck: oa check prints what is not available, and exits 1 when a
// dependency is held or not found (unknown only with --fail-on-unknown).
func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pkgs, dev []string
		flags     []string
		wantCode  int
		wantOut   []string
		wantNames []string // the packages the request asked about
	}{
		{
			name: "everything available", pkgs: []string{"guzzlehttp/guzzle", "monolog/monolog"},
			wantOut: []string{"All 2 dependencies of"}, wantNames: []string{"guzzlehttp/guzzle", "monolog/monolog"},
		},
		{
			name: "held and missing fail", pkgs: []string{"held/guzzle", "missing/pkg", "ok/pkg"}, wantCode: 1,
			wantOut: []string{
				"held/guzzle", "blocked by repository curation: EOL, use 7.x",
				"not_found  missing/pkg", "3 dependencies: 1 available, 1 held, 1 not found, 0 unknown.",
			},
		},
		{name: "no registry dependencies", wantOut: []string{"No registry dependencies to check in"}},
		{name: "unknown is reported but passes", pkgs: []string{"unknown/pkg"}, wantOut: []string{"unknown  unknown/pkg", "1 dependency: 0 available, 0 held, 0 not found, 1 unknown."}},
		{
			name: "unknown fails with --fail-on-unknown", pkgs: []string{"unknown/pkg"}, flags: []string{"--fail-on-unknown"},
			wantCode: 1, wantOut: []string{"0 not found, 1 unknown."},
		},
		{
			name: "--no-dev leaves out development dependencies", pkgs: []string{"ok/app"}, dev: []string{"held/phpunit"},
			flags: []string{"--no-dev"}, wantOut: []string{"All 1 dependency of"}, wantNames: []string{"ok/app"},
		},
		{
			name: "development dependencies are checked by default", pkgs: []string{"ok/app"}, dev: []string{"held/phpunit"},
			wantCode: 1, wantOut: []string{"held/phpunit"}, wantNames: []string{"held/phpunit", "ok/app"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newCheckServer(t, 0)
			args := append([]string{"check", "php", "--lockfile", composerLock(t, tc.pkgs, tc.dev)}, tc.flags...)

			res := runOA(t, srv.Server, "", args...)
			if res.code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", res.code, tc.wantCode, res.stdout, res.stderr)
			}

			for _, want := range tc.wantOut {
				if !strings.Contains(res.stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, res.stdout)
				}
			}

			if tc.wantNames != nil && (len(srv.requests) != 1 || strings.Join(srv.requests[0], ",") != strings.Join(tc.wantNames, ",")) {
				t.Errorf("requests = %v, want one asking for %v", srv.requests, tc.wantNames)
			}
		})
	}
}

// TestCheckBatchesAndJSON: more than 1000 dependencies go in requests of at most
// 1000, and --format json prints every result with a summary.
func TestCheckBatchesAndJSON(t *testing.T) {
	pkgs := make([]string, 1001)
	for i := range pkgs {
		pkgs[i] = fmt.Sprintf("ok/pkg%04d", i)
	}

	srv := newCheckServer(t, 0)

	res := runOA(t, srv.Server, "", "--format", "json", "check", "php", "--lockfile", composerLock(t, pkgs, nil))
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	if len(srv.requests) != 2 || len(srv.requests[0]) != 1000 || len(srv.requests[1]) != 1 {
		sizes := make([]int, len(srv.requests))
		for i, r := range srv.requests {
			sizes[i] = len(r)
		}

		t.Fatalf("request sizes = %v, want [1000 1]", sizes)
	}

	var report checkReport
	if err := json.Unmarshal([]byte(res.stdout), &report); err != nil {
		t.Fatalf("decode %q: %v", res.stdout, err)
	}

	if len(report.Results) != 1001 || report.Summary["available"] != 1001 || report.Kind != "composer" || report.Repository != "php" {
		t.Errorf(
			"report: %d results, summary %v, kind %q, repository %q",
			len(report.Results),
			report.Summary,
			report.Kind,
			report.Repository,
		)
	}
}

// TestCheckRefuses: a missing flag, a file that is not a known lockfile, an
// invalid lockfile and a server error fail before or instead of a verdict.
func TestCheckRefuses(t *testing.T) {
	dir := t.TempDir()
	unknown := filepath.Join(dir, "Pipfile.lock")
	yarn := filepath.Join(dir, "yarn.lock")
	broken := filepath.Join(dir, "package-lock.json")
	testdata := filepath.Join("..", "..", "pkg", "lockfile", "testdata")

	for path, data := range map[string]string{unknown: "{}", yarn: "# yarn lockfile v1", broken: "{not json"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name    string
		args    []string
		status  int
		wantErr string
	}{
		{"no lockfile", []string{"check", "php"}, 0, "--lockfile is required"},
		{"unknown lockfile", []string{"check", "php", "--lockfile", unknown}, 0, "unknown lockfile"},
		{"--no-dev with yarn.lock", []string{"check", "npm", "--lockfile", yarn, "--no-dev"}, 0, "does not record which dependencies"},
		{"--no-dev with Cargo.lock", []string{"check", "crates", "--lockfile", filepath.Join(testdata, "Cargo.lock"), "--no-dev"},
			0, "does not record which dependencies"},
		{"--no-dev with a Poetry 1.8 lockfile", []string{"check", "pypi", "--lockfile", filepath.Join(testdata, "poetry-1.8", "poetry.lock"), "--no-dev"},
			0, "does not record which dependencies"},
		{"invalid lockfile", []string{"check", "php", "--lockfile", broken}, 0, "invalid lockfile"},
		{"result missing", []string{"check", "php", "--lockfile", composerLock(t, []string{"short/a", "short/b"}, nil)}, 0, "unexpected availability response"},
		{"server error", []string{"check", "php", "--lockfile", composerLock(t, []string{"ok/a"}, nil)}, http.StatusForbidden, "no read access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newCheckServer(t, tc.status)

			res := runOA(t, srv.Server, "", tc.args...)
			if res.code != 1 || !strings.Contains(res.stderr, tc.wantErr) {
				t.Errorf("exit %d, stderr %q, want 1 and %q", res.code, res.stderr, tc.wantErr)
			}
		})
	}
}

// composerLockWith writes a composer.lock whose packages carry the given
// requirements (name -> required names), all at version 1.0.0.
func composerLockWith(t *testing.T, requires map[string][]string) string {
	t.Helper()

	var pkgs []map[string]any

	for name, req := range requires {
		r := map[string]string{"php": "^8.2"}
		for _, n := range req {
			r[n] = "^1.0"
		}

		pkgs = append(pkgs, map[string]any{"name": name, "version": "1.0.0", "require": r, "dist": map[string]any{"type": "zip"}})
	}

	data, err := json.Marshal(map[string]any{"packages": pkgs})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "composer.lock")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// TestCheckRequiredBy: a dependency that is not available says which locked
// packages pull it in, "-" when none does, in the table and in the JSON; the
// request body carries only package and version.
func TestCheckRequiredBy(t *testing.T) {
	lock := composerLockWith(t, map[string][]string{
		"acme/app-dbal": {"held/cache"}, "acme/app-orm": {"held/cache"}, "held/cache": nil, "held/direct": nil,
	})
	srv := newCheckServer(t, 0)

	res := runOA(t, srv.Server, "", "check", "php", "--lockfile", lock)
	if res.code != 1 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	if want := map[string]string{
		"held/cache":  "acme/app-dbal, acme/app-orm",
		"held/direct": "-",
	}; !maps.Equal(
		requiredByColumn(res.stdout),
		want,
	) {
		t.Errorf("REQUIRED BY = %v, want %v:\n%s", requiredByColumn(res.stdout), want, res.stdout)
	}

	if len(srv.bodies) != 1 {
		t.Fatalf("%d requests, want 1", len(srv.bodies))
	}

	var body struct {
		Packages []map[string]any `json:"packages"`
	}
	if err := json.Unmarshal([]byte(srv.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}

	for _, q := range body.Packages {
		if keys := slices.Sorted(maps.Keys(q)); !slices.Equal(keys, []string{"package", "version"}) {
			t.Errorf("request entry has keys %v, want package and version only", keys)
		}
	}

	js := runOA(t, newCheckServer(t, 0).Server, "", "--format", "json", "check", "php", "--lockfile", lock)

	var report checkReport
	if err := json.Unmarshal([]byte(js.stdout), &report); err != nil {
		t.Fatal(err)
	}

	for _, r := range report.Results {
		if r.Package == "held/cache" && strings.Join(r.RequiredBy, ",") != "acme/app-dbal,acme/app-orm" {
			t.Errorf("held/cache required_by = %v", r.RequiredBy)
		}
	}
}

// requiredByColumn maps each package of the table in out to its REQUIRED BY
// cell, located by the header's column offsets.
func requiredByColumn(out string) map[string]string {
	lines := strings.Split(out, "\n")
	cells := map[string]string{}

	for i, line := range lines {
		if !strings.HasPrefix(line, "STATUS") {
			continue
		}

		pkgAt, byAt, reasonAt := strings.Index(line, "PACKAGE"), strings.Index(line, "REQUIRED BY"), strings.Index(line, "REASON")

		for _, row := range lines[i+1:] {
			if len(row) < reasonAt {
				break
			}

			pkg := strings.Fields(row[pkgAt:])[0]
			cells[pkg] = strings.TrimSpace(row[byAt:reasonAt])
		}
	}

	return cells
}

// TestCheckFetch: --fetch requests each distinct package's metadata through the
// repository before it checks (composer p2 documents, npm packuments with a
// scoped name's slash escaped), counts what did not answer 200, and says so;
// without --fetch nothing is requested.
func TestCheckFetch(t *testing.T) {
	srv := newCheckServer(t, 0)

	res := runOA(t, srv.Server, "", "check", "php", "--lockfile",
		composerLock(t, []string{"ok/a", "missing/b"}, []string{"ok/a"}), "--fetch")
	if res.code != 1 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}

	got := append([]string{}, srv.gets...)
	slices.Sort(got)

	if want := []string{"/repos/php/p2/missing/b.json", "/repos/php/p2/ok/a.json"}; !slices.Equal(got, want) {
		t.Errorf("metadata requests = %v, want %v", got, want)
	}

	if !strings.Contains(res.stdout, "Fetched the metadata of 2 packages through php (1 did not answer 200).") {
		t.Errorf("stdout lacks the fetch summary:\n%s", res.stdout)
	}

	plain := newCheckServer(t, 0)
	lock := composerLock(t, []string{"ok/a"}, nil)

	if res := runOA(t, plain.Server, "", "check", "php", "--lockfile", lock); res.code != 0 || len(plain.gets) != 0 {
		t.Errorf("without --fetch: exit %d, metadata requested %v", res.code, plain.gets)
	}
}

// TestCheckLockfiles: every lockfile kind is checked like composer.lock, and
// --fetch requests each package's metadata document once.
func TestCheckLockfiles(t *testing.T) {
	for _, tc := range []struct {
		lock, repo string
		noDev      bool
		deps, gets int
		sample     string
	}{
		{"pnpm-lock.yaml", "npm", false, 13, 11, "/repos/npm/@acme%2fis"},
		{"yarn.lock", "npm", false, 13, 11, "/repos/npm/@acme%2fis"},
		{"uv.lock", "pypi", false, 14, 14, "/repos/pypi/simple/acme-http/"},
		{"uv.lock", "pypi", true, 6, 6, "/repos/pypi/simple/acme-http/"},
		{"poetry.lock", "pypi", true, 6, 6, "/repos/pypi/simple/acme-url/"},
		{"poetry-1.8/poetry.lock", "pypi", false, 14, 14, "/repos/pypi/simple/acme-charset/"},
		{"Cargo.lock", "crates", false, 9, 9, "/repos/crates/index/ac/me/acme_serde_derive"},
		{"Gemfile.lock", "gems", false, 5, 5, "/repos/gems/info/acme-xml"},
		{"packages.lock.json", "nuget", false, 4, 4, "/repos/nuget/v3/package/acme.log.console/index.json"},
		{"pylock.toml", "pypi", false, 13, 13, "/repos/pypi/simple/acme-http/"},
	} {
		t.Run(fmt.Sprintf("%s no-dev=%v", tc.lock, tc.noDev), func(t *testing.T) {
			srv := newCheckServer(t, 0)

			args := []string{"check", tc.repo, "--lockfile", filepath.Join("..", "..", "pkg", "lockfile", "testdata", tc.lock), "--fetch"}
			if tc.noDev {
				args = append(args, "--no-dev")
			}

			res := runOA(t, srv.Server, "", args...)
			if res.code != 0 || !strings.Contains(res.stdout, fmt.Sprintf("All %d dependencies of", tc.deps)) {
				t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
			}

			if !slices.Contains(srv.gets, tc.sample) || len(srv.gets) != tc.gets {
				t.Errorf("metadata requests = %v, want %d with %s", srv.gets, tc.gets, tc.sample)
			}
		})
	}
}

// TestMetadataPath: the document --fetch requests is the one that lists the
// locked version: Composer's "~dev" document for a branch, the npm packument
// with a scoped name's slash escaped.
func TestMetadataPath(t *testing.T) {
	for _, tc := range []struct {
		kind          lockfile.Kind
		name, version string
		want          string
	}{
		{lockfile.Composer, "acme/a", "1.2.0", "/repos/r%2Fx/p2/acme/a.json"},
		{lockfile.Composer, "acme/a", "v2.0.0-beta1", "/repos/r%2Fx/p2/acme/a.json"},
		{lockfile.Composer, "acme/a", "dev-main", "/repos/r%2Fx/p2/acme/a~dev.json"},
		{lockfile.Composer, "acme/a", "1.x-dev", "/repos/r%2Fx/p2/acme/a~dev.json"},
		{lockfile.NPM, "@scope/pkg", "1.0.0", "/repos/r%2Fx/@scope%2fpkg"},
		{lockfile.NPM, "plain", "1.0.0", "/repos/r%2Fx/plain"},
		{lockfile.UV, "acme-http", "2.32.3", "/repos/r%2Fx/simple/acme-http/"},
		{lockfile.Poetry, "acme-http", "2.32.3", "/repos/r%2Fx/simple/acme-http/"},
		{lockfile.Cargo, "a", "1.0.0", "/repos/r%2Fx/index/1/a"},
		{lockfile.Cargo, "abc", "1.0.0", "/repos/r%2Fx/index/3/a/abc"},
		{lockfile.Cargo, "Serde_Json", "1.0.0", "/repos/r%2Fx/index/se/rd/serde_json"},
		// What is left of a name is escaped: it stays the package part of the path.
		{lockfile.NPM, "a?x=1#y", "1.0.0", "/repos/r%2Fx/a%3Fx=1%23y"},
		{lockfile.NPM, "@s/p q%2f", "1.0.0", "/repos/r%2Fx/@s%2fp%20q%252f"},
		{lockfile.Bundler, "a?b", "1.0.0", "/repos/r%2Fx/info/a%3Fb"},
		{lockfile.Composer, "acme/a?b", "1.0.0", "/repos/r%2Fx/p2/acme/a%3Fb.json"},
		{lockfile.Bundler, "acme-xml", "1.16.7", "/repos/r%2Fx/info/acme-xml"},
		{lockfile.NuGet, "Acme.Log", "4.0.0", "/repos/r%2Fx/v3/package/acme.log/index.json"},
	} {
		if got, ok := metadataPath("r/x", tc.kind, lockfile.Dep{Name: tc.name, Version: tc.version}); got != tc.want || !ok {
			t.Errorf("metadataPath(%s, %s %s) = %q, %v; want %q", tc.kind, tc.name, tc.version, got, ok, tc.want)
		}
	}
}

// TestCheckWithoutAGraph: a pylock.toml that lists no dependencies (uv export,
// pip lock) says nothing about who requires what, so the table has no REQUIRED
// BY column, says why, and the JSON report says so; one that lists them keeps
// the column.
func TestCheckWithoutAGraph(t *testing.T) {
	for _, tc := range []struct {
		name, deps string
		graph      bool
	}{
		{"without dependency lists", "", false},
		{"with dependency lists", "dependencies = [{ name = \"held/x\" }]\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "pylock.toml")

			data := "lock-version = \"1.0\"\n\n[[packages]]\nname = \"held/x\"\nversion = \"1.0\"\n\n" +
				"[[packages]]\nname = \"ok/y\"\nversion = \"2.0\"\n" + tc.deps
			if err := os.WriteFile(lock, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}

			srv := newCheckServer(t, 0)

			res := runOA(t, srv.Server, "", "check", "pypi", "--lockfile", lock)
			if res.code != 1 || !strings.Contains(res.stdout, "held/x") ||
				strings.Contains(res.stdout, "REQUIRED BY") != tc.graph ||
				strings.Contains(res.stdout, "does not record which package requires which") == tc.graph {
				t.Fatalf("exit %d, stdout:\n%s", res.code, res.stdout)
			}

			if tc.graph {
				if got := requiredByColumn(res.stdout)["held/x"]; got != "ok/y" {
					t.Errorf("REQUIRED BY of held/x = %q, want ok/y", got)
				}
			}

			res = runOA(t, srv.Server, "", "--format", "json", "check", "pypi", "--lockfile", lock)

			var report checkReport
			if err := json.Unmarshal([]byte(res.stdout), &report); err != nil || report.Graph != tc.graph {
				t.Errorf("dependency_graph = %v (%v), want %v:\n%s", report.Graph, err, tc.graph, res.stdout)
			}
		})
	}
}

// TestMetadataPathRefusesNonNames: a lockfile cannot aim --fetch at another
// path: a name with a dot segment, an empty segment, a control character or a
// "/" its format does not have is not requested.
func TestMetadataPathRefusesNonNames(t *testing.T) {
	for _, tc := range []struct {
		kind lockfile.Kind
		name string
	}{
		{lockfile.NPM, ""},
		{lockfile.NPM, "../../api/v1/admin/users"},
		{lockfile.NPM, "@scope/../x"},
		{lockfile.NPM, "a/b"},
		{lockfile.NPM, "@a/b/c"},
		{lockfile.NPM, "a//b"},
		{lockfile.NPM, "a\x1b[2K"},
		{lockfile.Composer, "acme/.."},
		{lockfile.Composer, "acme/a/b"},
		{lockfile.UV, "a/b"},
		{lockfile.Cargo, "../x"},
		{lockfile.Bundler, "."},
		{lockfile.NuGet, "A/../B"},
	} {
		if got, ok := metadataPath("r", tc.kind, lockfile.Dep{Name: tc.name, Version: "1.0.0"}); ok {
			t.Errorf("metadataPath(%s, %q) = %q, want it refused", tc.kind, tc.name, got)
		}
	}
}

// TestCheckDistrustsTheLockfile: a crafted lockfile neither aims --fetch at
// another path nor draws on the terminal, and a status this oa does not know
// fails the check instead of passing as "nothing held".
func TestCheckDistrustsTheLockfile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		t.Helper()

		path := filepath.Join(dir, name, "package-lock.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}

		return path
	}

	crafted := write("crafted", `{"lockfileVersion":3,"packages":{`+
		`"node_modules/a":{"name":"../../api/v1/users","version":"1.0.0"},`+
		`"node_modules/b":{"name":"held/x\u001b[2K\rAll 9 dependencies are available\u202e","version":"1.0.0"},`+
		`"node_modules/c":{"name":"ok/y?z=1","version":"1.0.0"},`+
		`"node_modules/d":{"name":"plain?z=1","version":"1.0.0"}}}`)

	srv := newCheckServer(t, 0)

	res := runOA(t, srv.Server, "", "check", "npm", "--lockfile", crafted, "--fetch")
	if res.code != 1 {
		t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}

	for _, get := range srv.gets {
		if !strings.HasPrefix(get, "/repos/npm/") || strings.Contains(get, "..") || strings.Contains(get, "?") {
			t.Errorf("--fetch requested %q", get)
		}
	}

	if !slices.Equal(srv.gets, []string{"/repos/npm/plain%3Fz=1"}) {
		t.Errorf("--fetch requested %v, want only the escaped plain name", srv.gets)
	}

	if !strings.Contains(res.stdout, "4 packages through npm (3 did not answer 200)") {
		t.Errorf("the names that are no package names were not counted as not fetched:\n%s", res.stdout)
	}

	if strings.ContainsAny(res.stdout, "\x1b\r\u202e") || !strings.Contains(res.stdout, "held/x?[2K?All 9 dependencies are available?") {
		t.Errorf("the table carries control characters, or lost the name:\n%q", res.stdout)
	}

	weird := write("weird", `{"lockfileVersion":3,"packages":{"node_modules/a":{"name":"weird/x","version":"1.0.0"}}}`)

	res = runOA(t, srv.Server, "", "check", "npm", "--lockfile", weird)
	if res.code != 1 || !strings.Contains(res.stderr, "unknown status") {
		t.Errorf("unknown status: exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
}

// TestMetadataPathCargoNames: the index path is cut from the crate name's
// bytes, so a name that is not a crate name is not requested (".00" and "..ab"
// would have put a "." or ".." segment in it).
func TestMetadataPathCargoNames(t *testing.T) {
	for _, name := range []string{".00", "..ab", "-ab", "1ab", "é", "ab\u00e9cd", "a b"} {
		if got, ok := metadataPath("r", lockfile.Cargo, lockfile.Dep{Name: name, Version: "1.0.0"}); ok {
			t.Errorf("metadataPath(cargo, %q) = %q, want it rejected", name, got)
		}
	}

	if got, ok := metadataPath("r", lockfile.Cargo, lockfile.Dep{Name: "Serde_json", Version: "1.0.0"}); !ok ||
		got != "/repos/r/index/se/rd/serde_json" {
		t.Errorf("metadataPath(cargo, Serde_json) = %q, %v", got, ok)
	}
}
