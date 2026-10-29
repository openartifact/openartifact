// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSafeClientCrossOriginRedirectDropsCustomHeaders drives real redirects
// between two origins: a custom credential header is not seen by the second
// origin, the allowlisted ones are, and a replayed body is refused.
func TestSafeClientCrossOriginRedirectDropsCustomHeaders(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	type seenReq struct {
		header http.Header
		body   string
	}

	cases := []struct {
		name    string
		method  string
		status  int
		body    string
		wantErr error
	}{
		{"get 302", http.MethodGet, http.StatusFound, "", nil},
		{"get 307", http.MethodGet, http.StatusTemporaryRedirect, "", nil},
		{"post 302 becomes get", http.MethodPost, http.StatusFound, "payload", nil},
		{"post 307 body refused", http.MethodPost, http.StatusTemporaryRedirect, "payload", errRedirectBody},
		{"put 308 body refused", http.MethodPut, http.StatusPermanentRedirect, "payload", errRedirectBody},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan seenReq, 1)
			second := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				seen <- seenReq{r.Header.Clone(), string(b)}
			}))
			defer second.Close()

			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, second.URL, tc.status)
			}))
			defer first.Close()

			req, err := http.NewRequestWithContext(t.Context(), tc.method, first.URL, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}

			req.Header.Set("X-Api-Key", "secret")
			req.Header.Set("Private-Token", "secret")
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Range", "bytes=1-")
			req.Header.Set("User-Agent", "OpenArtifact-Proxy")

			resp, err := SafeHTTPClient(5 * time.Second).Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}

				select {
				case got := <-seen:
					t.Fatalf("the second origin was contacted: %v %q", got.header, got.body)
				default:
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			got := <-seen
			for _, h := range []string{"X-Api-Key", "Private-Token", "Authorization"} {
				if v := got.header.Get(h); v != "" {
					t.Errorf("%s leaked to the second origin: %q", h, v)
				}
			}

			for h, want := range map[string]string{"Accept": "application/json", "Range": "bytes=1-", "User-Agent": "OpenArtifact-Proxy"} {
				if v := got.header.Get(h); v != want {
					t.Errorf("%s = %q, want %q (allowlisted headers survive)", h, v, want)
				}
			}

			if got.body != "" {
				t.Errorf("body %q reached the second origin", got.body)
			}
		})
	}
}

// TestSafeClientSameOriginRedirectKeepsHeaders: the allowlist only applies when
// the origin changes.
func TestSafeClientSameOriginRedirectKeepsHeaders(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	got := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			got <- r.Header.Get("X-Api-Key")

			return
		}

		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("X-Api-Key", "secret")

	resp, err := SafeHTTPClient(5 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if v := <-got; v != "secret" {
		t.Errorf("same-origin redirect dropped X-Api-Key: %q", v)
	}
}
