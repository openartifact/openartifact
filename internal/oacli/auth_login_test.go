// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAuthServer mimics the session login flow: cookie session + CSRF cookie,
// a forced password change for the bootstrap account, and token minting that
// is refused until the password has been changed.
func fakeAuthServer(t *testing.T, mustChange bool) *httptest.Server {
	t.Helper()

	password := "bootstrap-pw"
	changed := !mustChange

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)

		if b["username"] != "admin" || b["password"] != password {
			http.Error(w, `{"error":"invalid username or password"}`, http.StatusUnauthorized)
			return
		}

		http.SetCookie(w, &http.Cookie{Name: "oa_session", Value: "s", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "oa_csrf", Value: "csrf-1", Path: "/"})
		_ = json.NewEncoder(w).Encode(map[string]any{"must_change_password": !changed})
	})

	mux.HandleFunc("POST /api/v1/auth/change-password", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Csrf-Token") != "csrf-1" {
			http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
			return
		}

		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)

		if b["current_password"] != password {
			http.Error(w, `{"error":"bad current"}`, http.StatusUnauthorized)
			return
		}

		password, changed = b["new_password"], true

		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/v1/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)

		switch {
		case !changed:
			http.Error(w, `{"error":"password_change_required"}`, http.StatusForbidden)
		case r.Header.Get("X-Csrf-Token") != "csrf-1":
			http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
		case b["kind"] != "access" || b["name"] == "":
			http.Error(w, `{"error":"kind must be 'access' or 'deploy'"}`, http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"oa_tok_123","id":"tok-1"}`))
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func withStdin(t *testing.T, input string) {
	t.Helper()

	orig := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(input))

	t.Cleanup(func() { stdinReader = orig })
}

func TestSessionLoginTokenPlain(t *testing.T) {
	srv := fakeAuthServer(t, false)

	got, _ := sessionLoginToken(newClient(srv.URL, ""), "admin", "bootstrap-pw", true)
	if got != "oa_tok_123" {
		t.Fatalf("token = %q", got)
	}
}

func TestSessionLoginTokenForcedPasswordChange(t *testing.T) {
	srv := fakeAuthServer(t, true)
	withStdin(t, "brand-new-password\n")

	got, _ := sessionLoginToken(newClient(srv.URL, ""), "admin", "bootstrap-pw", true)
	if got != "oa_tok_123" {
		t.Fatalf("token = %q", got)
	}
}

func TestSessionLoginTokenReturnsID(t *testing.T) {
	srv := fakeAuthServer(t, false)

	_, id := sessionLoginToken(newClient(srv.URL, ""), "admin", "bootstrap-pw", true)
	if id != "tok-1" {
		t.Fatalf("token id = %q", id)
	}
}

func TestTolerantOfBadConfig(t *testing.T) {
	srv := fakeAuthServer(t, false)

	cases := []struct {
		name string
		args []string
		ok   bool
	}{
		{"login", []string{"--server", srv.URL, "auth", "login", "--username", "admin", "--password-stdin"}, true},
		{
			"login with the local server flag",
			[]string{"auth", "login", "--server", srv.URL, "--username", "admin", "--password-stdin"},
			true,
		},
		{"version", []string{"version"}, true},
		{"help", []string{"help"}, true},
		{"repo list", []string{"repo", "list"}, false},
		{"logout", []string{"auth", "logout"}, false},
		{"token flag", []string{"--token", "t", "webhook", "list"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("OA_SERVER", "")
			t.Setenv("OA_TOKEN", "")

			path := filepath.Join(home, ".config", "oa", "config.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}

			withStdin(t, "bootstrap-pw\n")

			var stderr bytes.Buffer

			code := 0

			captureStdout(t, func() { code = run(tc.args, os.Stdout, &stderr) })

			if (code == 0) != tc.ok {
				t.Fatalf("exit %d, stderr %q; want success=%v", code, stderr.String(), tc.ok)
			}

			if !tc.ok && !strings.Contains(stderr.String(), "loading config") {
				t.Errorf("stderr = %q, want the config error", stderr.String())
			}
		})
	}
}
