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

	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.EscapedPath(), "/repos/php/") {
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

	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repositories/php/availability" {
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
	yarn := filepath.Join(dir, "yarn.lock")
	broken := filepath.Join(dir, "package-lock.json")

	for path, data := range map[string]string{yarn: "# yarn", broken: "{not json"} {
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
		{"unknown lockfile", []string{"check", "php", "--lockfile", yarn}, 0, "unknown lockfile"},
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
	} {
		if got := metadataPath("r/x", tc.kind, lockfile.Dep{Name: tc.name, Version: tc.version}); got != tc.want {
			t.Errorf("metadataPath(%s, %s %s) = %q, want %q", tc.kind, tc.name, tc.version, got, tc.want)
		}
	}
}
