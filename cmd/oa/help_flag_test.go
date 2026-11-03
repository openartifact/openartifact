// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// usageCommands returns every command of the tree as its words ("repo create",
// "auth token revoke", "upload", ...).
func usageCommands(t *testing.T) [][]string {
	t.Helper()

	var cmds [][]string

	for _, c := range walkCommands(newRootCmd()) {
		cmds = append(cmds, words(c))
	}

	if len(cmds) < 100 {
		t.Fatalf("only %d commands in the tree", len(cmds))
	}

	return cmds
}

func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var n atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	return srv, &n
}

// Every command prints its help and exits 0 for -h and --help, wherever the
// flag sits, without a single request: a help flag must never be taken for a
// name, id or path.
func TestEveryCommandHelpMakesNoRequest(t *testing.T) {
	srv, requests := countingServer(t)

	// Help is decided before any command code runs, so one flag spelling and
	// position per command (alternating) covers every command without
	// spawning hundreds of processes under -race.
	t.Run("commands", func(t *testing.T) {
		for i, words := range usageCommands(t) {
			name := strings.Join(words, " ")
			h := []string{"--help", "-h"}[i%2]

			variants := [][]string{
				append(append([]string{}, words...), h),
				append(append([]string{}, words...), h, "x"),
				append(append([]string{}, words...), "x", "y", h),
			}
			args := variants[i%3]

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				res := runOA(t, srv, "", args...)
				if res.code != 0 || !strings.Contains(res.stdout, words[0]) {
					t.Errorf("oa %v: exit %d, stdout %q, stderr %q", args, res.code, res.stdout, res.stderr)
				}
			})
		}
	})

	// Every spelling and position, on the command that once misbehaved.
	for _, h := range []string{"--help", "-h"} {
		for _, args := range [][]string{{"repo", "create", h}, {"repo", "create", h, "x"}, {"repo", "create", "x", h}, {"repo", h}, {h}} {
			if res := runOA(t, srv, "", args...); res.code != 0 {
				t.Errorf("oa %v: exit %d, stderr %q", args, res.code, res.stderr)
			}
		}
	}

	if n := requests.Load(); n != 0 {
		t.Fatalf("help made %d HTTP requests, want 0", n)
	}
}

func TestHelpShowsOnlyTheSubcommand(t *testing.T) {
	srv, _ := countingServer(t)

	res := runOA(t, srv, "", "repo", "create", "--help")
	if !strings.Contains(res.stdout, "repo create <name> --format") || strings.Contains(res.stdout, "repo show") {
		t.Errorf("unexpected help: %q", res.stdout)
	}
}

// A flag in the position of a name is refused before any request is made.
func TestFlagAsNameIsRefused(t *testing.T) {
	srv, requests := countingServer(t)

	cases := [][]string{
		{"repo", "create", "--format", "npm"},
		{"repo", "create", "--bogus"},
		{"repo", "show", "--bogus"},
		{"repo", "update", "--bogus"},
		{"repo", "delete", "--bogus"},
		{"repo", "packages", "-x"},
		{"repo", "members", "-x"},
		{"group", "create", "--bogus"},
		{"group", "show", "--bogus"},
		{"project", "show", "--bogus"},
		{"user", "show", "-x"},
		{"webhook", "show", "-x"},
		{"storage", "show", "-x"},
		{"artifact", "list", "-x"},
		{"artifact", "delete", "-x", "id"},
		{"sbom", "list", "-x"},
		{"trash", "restore", "-x", "id"},
	}

	for _, args := range cases {
		res := runOA(t, srv, "", args...)
		if res.code == 0 {
			t.Errorf("oa %s: exit 0, want failure", strings.Join(args, " "))
		}
	}

	if n := requests.Load(); n != 0 {
		t.Fatalf("a flag used as a name reached the server %d times", n)
	}
}

func TestRepoCreateRequiresFormat(t *testing.T) {
	srv, requests := countingServer(t)

	res := runOA(t, srv, "", "repo", "create", "myrepo")
	if res.code == 0 || !strings.Contains(res.stderr, "--format is required") || !strings.Contains(res.stderr, "npm") {
		t.Errorf("exit %d, stderr %q", res.code, res.stderr)
	}

	if requests.Load() != 0 {
		t.Error("request made without --format")
	}
}

// Flags may follow the query: `oa search q --fmt rpm` filters like
// `oa search --fmt rpm q`, instead of searching for "q --fmt rpm".
func TestSearchFlagsAfterQuery(t *testing.T) {
	var last atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last.Store(r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(srv.Close)

	runOA(t, srv, "", "search", "zzcheck", "--fmt", "rpm", "--limit", "3")

	got, _ := last.Load().(string)

	for _, want := range []string{"q=zzcheck&", "format=rpm", "limit=3"} {
		if !strings.Contains(got+"&", want) {
			t.Errorf("query %q lacks %q", got, want)
		}
	}
}

func TestGlobalFormatAfterCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"repositories":[{"name":"r","format":"npm"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := runOA(t, srv, "", "repo", "list", "--format", "json")
	if res.code != 0 || !strings.Contains(res.stdout, `"name": "r"`) {
		t.Errorf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}
}

func TestQuarantineReleaseBoolForm(t *testing.T) {
	var last atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last.Store(string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	res := runOA(t, srv, "", "vuln", "quarantine", "r", "id", "--release=false")
	body, _ := last.Load().(string)
	if res.code != 0 || !strings.Contains(body, `"quarantined":true`) {
		t.Errorf("--release=false: exit %d, body %q, stderr %q", res.code, body, res.stderr)
	}

	res = runOA(t, srv, "", "vuln", "quarantine", "r", "id", "--release")
	body, _ = last.Load().(string)
	if res.code != 0 || !strings.Contains(body, `"quarantined":false`) {
		t.Errorf("--release: exit %d, body %q, stderr %q", res.code, body, res.stderr)
	}
}

func TestAuditExportToDevNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}\n"))
	}))
	t.Cleanup(srv.Close)

	if res := runOA(t, srv, "", "audit", "export", "-o", os.DevNull); res.code != 0 {
		t.Errorf("exit %d, stderr %q", res.code, res.stderr)
	}
}

func TestVulnScanStillScanningMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"s1","scan_status":"scanning","vuln_count":0}`))
	}))
	t.Cleanup(srv.Close)

	res := runOA(t, srv, "", "vuln", "scan", "r", "--artifact-id", "a@1", "--package", "p", "--version", "1", "--ecosystem", "npm")
	if res.code != 0 || !strings.Contains(res.stdout, "still running") || strings.Contains(res.stdout, "0 vulnerabilities") {
		t.Errorf("exit %d, stdout %q", res.code, res.stdout)
	}
}
