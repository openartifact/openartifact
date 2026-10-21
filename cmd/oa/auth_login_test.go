// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			_, _ = w.Write([]byte(`{"token":"oa_tok_123"}`))
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

	got := sessionLoginToken(newClient(srv.URL, ""), "admin", "bootstrap-pw", true)
	if got != "oa_tok_123" {
		t.Fatalf("token = %q", got)
	}
}

func TestSessionLoginTokenForcedPasswordChange(t *testing.T) {
	srv := fakeAuthServer(t, true)
	withStdin(t, "brand-new-password\n")

	got := sessionLoginToken(newClient(srv.URL, ""), "admin", "bootstrap-pw", true)
	if got != "oa_tok_123" {
		t.Fatalf("token = %q", got)
	}
}
