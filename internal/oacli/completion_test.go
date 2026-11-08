// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// complete runs cobra's hidden __complete command and returns the suggestions
// (without descriptions) and the directive line.
func complete(t *testing.T, args ...string) ([]string, string) {
	t.Helper()

	var out, errb bytes.Buffer

	if code := run(append([]string{"__complete"}, args...), &out, &errb); code != 0 {
		t.Fatalf("__complete %v: exit %d, stderr %q", args, code, errb.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], ":") {
		t.Fatalf("__complete %v: no directive in %q", args, out.String())
	}

	var got []string

	for _, l := range lines[:len(lines)-1] {
		name, _, _ := strings.Cut(l, "\t")
		got = append(got, name)
	}

	return got, lines[len(lines)-1]
}

// completionEnv points the CLI at srv with a token that must never be echoed.
func completionEnv(t *testing.T, server, token string) {
	t.Helper()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("OA_SERVER", server)
	t.Setenv("OA_TOKEN", token)
}

const secretToken = "oa_pat_must-never-appear"

func repoListServer(t *testing.T, status int, body string, delay time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var n atomic.Int64

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)

		if r.Header.Get("Authorization") != "Bearer "+secretToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)

			return
		}

		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-release:
				return
			case <-r.Context().Done():
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv, &n
}

func TestCompleteFlagEnums(t *testing.T) {
	srv, requests := countingServer(t)
	completionEnv(t, srv.URL, secretToken)

	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"repo", "list", "--format", ""}, []string{"table", "json"}},
		{[]string{"--format", ""}, []string{"table", "json"}},
		{[]string{"repo", "create", "r", "--format", ""}, []string{"npm", "maven", "docker", "pypi"}},
		{[]string{"audit", "export", "--format", ""}, []string{"jsonl", "csv"}},
		{[]string{"sbom", "generate", "r", "--format", ""}, []string{"cyclonedx", "spdx"}},
		{[]string{"completion", ""}, []string{"bash", "zsh", "fish", "powershell"}},
		{[]string{"repo", "cr"}, []string{"create"}},
	} {
		got, _ := complete(t, tc.args...)

		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("__complete %v = %v, want it to contain %q", tc.args, got, w)
			}
		}
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("static completion made %d requests", n)
	}

	if got, _ := complete(t, "repo", "create", "r", "--format", "np"); !slices.Equal(got, []string{"npm"}) {
		t.Errorf("prefix np = %v, want [npm]", got)
	}
}

func TestCompleteRepoNames(t *testing.T) {
	srv, _ := repoListServer(t, http.StatusOK, `{"repositories":[{"name":"alpha"},{"name":"beta"},{"name":"alpine"}],"total":3}`, 0)
	completionEnv(t, srv.URL, secretToken)

	for _, args := range [][]string{
		{"repo", "show", ""},
		{"repo", "delete", ""},
		{"artifact", "list", ""},
		{"upload", ""},
	} {
		got, directive := complete(t, args...)
		if !slices.Equal(got, []string{"alpha", "beta", "alpine"}) {
			t.Errorf("__complete %v = %v, want the three repositories", args, got)
		}

		if directive != ":4" {
			t.Errorf("__complete %v: directive %s, want :4 (no file completion)", args, directive)
		}
	}

	if got, _ := complete(t, "repo", "show", "al"); !slices.Equal(got, []string{"alpha", "alpine"}) {
		t.Errorf("prefix al = %v", got)
	}

	// Only the first positional is a repository: later ones are not suggested.
	if got, _ := complete(t, "repo", "show", "alpha", ""); len(got) != 0 {
		t.Errorf("second positional = %v, want none", got)
	}
}

func TestCompleteFilesForPathArguments(t *testing.T) {
	srv, _ := repoListServer(t, http.StatusOK, `{"repositories":[]}`, 0)
	completionEnv(t, srv.URL, secretToken)

	// The default directive (0) lets the shell complete file names.
	if _, directive := complete(t, "upload", "r", ""); directive != ":0" {
		t.Errorf("upload file argument: directive %s, want :0", directive)
	}
}

func TestCompleteIsEmptyWithoutServerOrToken(t *testing.T) {
	srv, requests := repoListServer(t, http.StatusInternalServerError, `{"error":"boom"}`, 0)

	t.Run("server error", func(t *testing.T) {
		completionEnv(t, srv.URL, secretToken)

		if got, _ := complete(t, "repo", "show", ""); len(got) != 0 {
			t.Errorf("suggestions after a 500: %v", got)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		completionEnv(t, srv.URL, "other")

		if got, _ := complete(t, "repo", "show", ""); len(got) != 0 {
			t.Errorf("suggestions after a 401: %v", got)
		}
	})

	t.Run("not json", func(t *testing.T) {
		bad, _ := repoListServer(t, http.StatusOK, `<html>`, 0)
		completionEnv(t, bad.URL, secretToken)

		if got, _ := complete(t, "repo", "show", ""); len(got) != 0 {
			t.Errorf("suggestions from a non-JSON reply: %v", got)
		}
	})

	t.Run("no server listening", func(t *testing.T) {
		completionEnv(t, "http://127.0.0.1:1", secretToken)

		if got, _ := complete(t, "repo", "show", ""); len(got) != 0 {
			t.Errorf("suggestions without a server: %v", got)
		}
	})

	t.Run("no token", func(t *testing.T) {
		before := requests.Load()

		completionEnv(t, srv.URL, "")

		if got, _ := complete(t, "repo", "show", ""); len(got) != 0 {
			t.Errorf("suggestions without a token: %v", got)
		}

		if requests.Load() != before {
			t.Error("completion without a token made a request")
		}
	})
}

// A server that never answers costs the user about two seconds, not a hang.
func TestCompleteGivesUpAfterTheTimeout(t *testing.T) {
	srv, _ := repoListServer(t, http.StatusOK, `{"repositories":[{"name":"slow"}]}`, time.Minute)
	completionEnv(t, srv.URL, secretToken)

	start := time.Now()
	got, _ := complete(t, "repo", "show", "")

	if len(got) != 0 {
		t.Errorf("suggestions from a stalled server: %v", got)
	}

	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("completion took %s, want about 2s", d)
	}
}

// Completion never prints the token, never prompts and never warns on stderr.
func TestCompleteNeverPrintsSecrets(t *testing.T) {
	srv, _ := repoListServer(t, http.StatusOK, `{"repositories":[{"name":"alpha"}]}`, 0)
	completionEnv(t, srv.URL, secretToken)
	withStdin(t, "")

	var out, errb bytes.Buffer

	args := []string{"__complete", "--token", secretToken, "repo", "show", ""}
	if code := run(args, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}

	if strings.Contains(out.String()+errb.String(), secretToken) {
		t.Errorf("the token was printed: stdout %q stderr %q", out.String(), errb.String())
	}

	if strings.Contains(errb.String(), "warning") {
		t.Errorf("completion printed a warning: %q", errb.String())
	}
}

func TestCompletionScripts(t *testing.T) {
	for shell, marker := range map[string]string{
		"bash":       "bash completion V2 for oa",
		"zsh":        "#compdef oa",
		"fish":       "fish completion for oa",
		"powershell": "Register-ArgumentCompleter",
	} {
		var out, errb bytes.Buffer

		if code := run([]string{"completion", shell}, &out, &errb); code != 0 || !strings.Contains(out.String(), marker) {
			t.Errorf("oa completion %s: exit %d, stderr %q, output lacks %q", shell, code, errb.String(), marker)
		}
	}

	// The install instructions are part of the help of every shell.
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var out, errb bytes.Buffer

		if code := run(
			[]string{"completion", shell, "--help"},
			&out,
			&errb,
		); code != 0 ||
			!strings.Contains(out.String(), "oa completion "+shell) {
			t.Errorf("oa completion %s --help: exit %d, output %q", shell, code, out.String())
		}
	}
}

func TestCompleteDoesNotSendSavedTokenToAnotherServer(t *testing.T) {
	var requests atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		if r.Header.Get("Authorization") != "" {
			t.Errorf("saved token sent to a different server: %q", r.Header.Get("Authorization"))
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("OA_SERVER", srv.URL)
	t.Setenv("OA_TOKEN", "")

	if err := saveConfig(&Config{Server: "https://prod.example.com", Token: secretToken}); err != nil {
		t.Fatal(err)
	}

	if got, _ := complete(t, "repo", "show", ""); len(got) != 0 {
		t.Errorf("suggestions without a token: %v", got)
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("completion made %d requests without a usable token", n)
	}
}
