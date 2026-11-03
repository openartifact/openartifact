// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingServer answers every request with an empty JSON object and records
// "METHOD path" of each.
func recordingServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()

	var (
		mu   sync.Mutex
		seen []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"repositories":[],"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), seen...)
	}
}

// After "--" an argument is positional even when it starts with "-": the name
// reaches the server, and "--" itself is never taken as a name.
func TestDoubleDashMakesDashNamesPositional(t *testing.T) {
	cases := []struct {
		args []string
		want string // a request path suffix
	}{
		{[]string{"repo", "show", "--", "-legacy2"}, "/-legacy2"},
		{[]string{"repo", "packages", "--", "-legacy2"}, "/-legacy2/"},
		{[]string{"repo", "delete", "--confirm", "-legacy2", "--", "-legacy2"}, "/-legacy2"},
		{[]string{"repo", "update", "--description", "d", "--", "-legacy2"}, "/-legacy2"},
		{[]string{"project", "show", "--", "-legacy2"}, "/-legacy2"},
		{[]string{"group", "show", "--", "-legacy2"}, "/-legacy2"},
		{[]string{"sbom", "list", "--", "-legacy2"}, "/-legacy2/"},
		{[]string{"vuln", "list", "--", "-legacy2"}, "/-legacy2/"},
		{[]string{"vuln", "quarantine", "--release", "--", "-legacy2", "id1"}, "/-legacy2/"},
		{[]string{"cleanup", "list", "--", "-legacy2"}, "/-legacy2/"},
		{[]string{"artifact", "list", "--", "-legacy2"}, "/-legacy2/"},
		{[]string{"search", "--repo", "-legacy2", "--", "-q"}, "q="},
	}

	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			srv, seen := recordingServer(t)

			res := runOA(t, srv, "", c.args...)
			if strings.Contains(res.stderr, "flag") {
				t.Fatalf("flag-like refusal after --: %q", res.stderr)
			}

			got := seen()
			if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), c.want) {
				t.Fatalf("requests %v, want one containing %q (stderr %q)", got, c.want, res.stderr)
			}

			for _, r := range got {
				if strings.Contains(r, "/--") || strings.HasSuffix(r, "/--") {
					t.Fatalf("-- was taken as a name: %v", got)
				}
			}
		})
	}
}

// Without "--" a leading-dash name is still refused, and says how to get past.
func TestDashNameWithoutDoubleDashIsRefusedWithHint(t *testing.T) {
	srv, requests := countingServer(t)

	res := runOA(t, srv, "", "repo", "delete", "-legacy2")
	if res.code == 0 || !strings.Contains(res.stderr, "use -- before names starting with -") {
		t.Fatalf("code %d stderr %q", res.code, res.stderr)
	}

	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestDoubleDashUploadPositionals(t *testing.T) {
	srv, _ := countingServer(t)

	dir := t.TempDir()
	res := runOA(t, srv, "", "upload", "--target-path", "x/", "--", "-r", dir+"/-none*.bin")

	if res.code == 0 || strings.Contains(res.stderr, "unknown shorthand flag") || strings.Contains(res.stderr, "unknown flag") {
		t.Fatalf("-r after -- must be positional: code %d stderr %q", res.code, res.stderr)
	}
}

// A missing positional is a usage error, never the confirmation message.
func TestMissingPositionalIsUsageError(t *testing.T) {
	srv, requests := countingServer(t)

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"trash", "purge", "repository", "--yes"}, "Usage: oa trash purge"},
		{[]string{"trash", "restore", "repository", "--repo", "x"}, "Usage: oa trash restore"},
	}

	for _, c := range cases {
		res := runOA(t, srv, "", c.args...)
		if res.code != 1 || !strings.Contains(res.stderr, c.want) || strings.Contains(res.stderr, "confirm") {
			t.Errorf("oa %s: code %d stderr %q, want usage %q", strings.Join(c.args, " "), res.code, res.stderr, c.want)
		}
	}

	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

// Unknown flags and invalid --format values fail with exit 1 and no request.
func TestUnknownFlagAndInvalidFormat(t *testing.T) {
	srv, requests := countingServer(t)

	cases := [][]string{
		{"repo", "list", "--bogusflag"},
		{"user", "list", "--bogusflag"},
		{"group", "list", "-x"},
		{"project", "list", "--bogusflag"},
		{"token", "list", "--bogusflag"},
		{"storage", "list", "--bogusflag"},
		{"backup", "list", "--bogusflag"},
		{"gpg", "list", "--bogusflag"},
		{"webhook", "list", "--bogusflag"},
		{"sbom", "list", "myrepo", "--bogusflag"},
		{"auth", "whoami", "--bogusflag"},
		{"system", "info", "--bogusflag"},
		{"repo", "list", "--format", "xml"},
		{"repo", "list", "--format=xml"},
		{"--format", "yaml", "repo", "list"},
		{"--format", "", "repo", "list"},
	}

	for _, args := range cases {
		res := runOA(t, srv, "", args...)
		if res.code != 1 {
			t.Errorf("oa %s: exit %d, want 1 (stderr %q)", strings.Join(args, " "), res.code, res.stderr)
		}
	}

	if n := requests.Load(); n != 0 {
		t.Fatalf("%d requests reached the server", n)
	}
}

func TestValidFormatsStillWork(t *testing.T) {
	srv, seen := recordingServer(t)

	for _, f := range []string{"table", "json"} {
		if res := runOA(t, srv, "", "repo", "list", "--format", f); res.code != 0 {
			t.Errorf("--format %s: exit %d: %s", f, res.code, res.stderr)
		}
	}

	if len(seen()) == 0 {
		t.Fatal("no request")
	}
}
