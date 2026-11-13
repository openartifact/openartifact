// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// checkServer answers POST /api/v1/repositories/{repo}/availability from the
// package names: held/* is held, missing/* not found, unknown/* unknown,
// anything else available. It records the package names of every request.
type checkServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests [][]string
	status   int // non-zero: answer every request with this status and an error; set before serving
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

	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repositories/php/availability" {
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		return
	}

	var body struct {
		Packages []struct{ Package, Version string } `json:"packages"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
				"held       held/guzzle  1.0.0    php-proxy  blocked by repository curation: EOL, use 7.x",
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
