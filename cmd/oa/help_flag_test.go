// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// usageCommands returns every command listed in the usage text, as the leading
// words of each entry ("repo create", "auth token revoke", "upload", ...).
func usageCommands(t *testing.T) [][]string {
	t.Helper()

	var cmds [][]string

	for l := range strings.SplitSeq(usageText(), "\n") {
		if !strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "   ") {
			continue
		}

		var words []string

		for f := range strings.FieldsSeq(l) {
			if strings.ContainsAny(f[:1], "<[-(") || len(words) == 3 {
				break
			}

			words = append(words, strings.Split(f, "|")[0])
		}

		if len(words) > 0 && words[0] != "oa" {
			cmds = append(cmds, words)
		}
	}

	if len(cmds) < 80 {
		t.Fatalf("parsed only %d commands from the usage text", len(cmds))
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

	for _, words := range usageCommands(t) {
		name := strings.Join(words, " ")

		for _, h := range []string{"--help", "-h"} {
			variants := map[string][]string{
				"trailing": append(append([]string{}, words...), h),
				"operand":  append(append([]string{}, words...), h, "x"),
				"after":    append(append([]string{}, words...), "x", "y", h),
			}

			for pos, args := range variants {
				res := runOA(t, srv, "", args...)
				if res.code != 0 {
					t.Errorf("oa %s (%s %s): exit %d, stderr %q", name, h, pos, res.code, res.stderr)
				}

				if !strings.Contains(res.stdout, words[0]) {
					t.Errorf("oa %s (%s %s): no help on stdout: %q", name, h, pos, res.stdout)
				}
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
		{"project", "create", "--display-name", "X", "p"},
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
	var got string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(srv.Close)

	runOA(t, srv, "", "search", "zzcheck", "--fmt", "rpm", "--limit", "3")

	for _, want := range []string{"q=zzcheck&", "format=rpm", "limit=3"} {
		if !strings.Contains(got+"&", want) {
			t.Errorf("query %q lacks %q", got, want)
		}
	}
}
