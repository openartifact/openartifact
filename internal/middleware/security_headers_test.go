// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	h := SecurityHeadersMiddleware(true)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	for k, v := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s=%q want %q", k, got, v)
		}
	}
	if w.Header().Get("Strict-Transport-Security") == "" || w.Header().Get("Content-Security-Policy") == "" {
		t.Error("missing HSTS/CSP")
	}
}

func TestSecurityHeadersArtifactsSandboxedAndAttached(t *testing.T) {
	serve := func(path, ct string) *httptest.ResponseRecorder {
		h := SecurityHeadersMiddleware(false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if ct != "" {
				w.Header().Set("Content-Type", ct)
			}

			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

		return w
	}

	for _, ct := range []string{"text/html", "text/html; charset=utf-8", "image/svg+xml", "application/xhtml+xml", "text/xml", "application/xml", ""} {
		w := serve("/repos/raw/r/x", ct)
		if got := w.Header().Get("Content-Disposition"); got != "attachment" {
			t.Errorf("%q: Content-Disposition=%q, want attachment", ct, got)
		}

		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("%q: CSP=%q lacks sandbox", ct, csp)
		}
	}

	for _, ct := range []string{"image/png", "application/pdf", "text/plain; charset=utf-8"} {
		if got := serve("/repos/raw/r/x", ct).Header().Get("Content-Disposition"); got != "" {
			t.Errorf("%q: unexpected Content-Disposition %q", ct, got)
		}
	}

	if got := serve("/api/v1/x", "text/html").Header().Get("Content-Disposition"); got != "" {
		t.Errorf("non-artifact path got Content-Disposition %q", got)
	}
}

func TestSecurityHeadersSPANavigationNotSandboxed(t *testing.T) {
	h := SecurityHeadersMiddleware(false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html></html>"))
	}))

	nav := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		r.Header.Set("Accept", "text/html")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "document")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		return w
	}

	for _, path := range []string{"/repos/r/settings", "/repos/r/packages/npm/@a%2Fb"} {
		w := nav(path)
		if strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("Content-Disposition") != "" {
			t.Errorf("%s: SPA page got sandbox/attachment: %v", path, w.Header())
		}
	}

	// A real artifact download (no navigation headers) is still locked down.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/raw/r/x.html", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("Content-Disposition") != "attachment" {
		t.Errorf("raw html download not sandboxed: %v", w.Header())
	}
}

// The OCI registry API has no SPA pages and serves manifests and listings
// whose types come from uploaders or upstreams: /v2/ is sandboxed and forced
// to download like /repos/ artifacts, even for a browser navigation.
func TestSecurityHeadersRegistryAPISandboxedAndAttached(t *testing.T) {
	serve := func(path, ct string, nav bool) *httptest.ResponseRecorder {
		h := SecurityHeadersMiddleware(false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		}))
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)

		if nav {
			r.Header.Set("Accept", "text/html")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
		}

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		return w
	}

	for _, path := range []string{"/v2/repo/img/manifests/latest", "/v2/repo/img/referrers/sha256:abc", "/v2/"} {
		for _, nav := range []bool{false, true} {
			w := serve(path, "text/html", nav)
			if got := w.Header().Get("Content-Disposition"); got != "attachment" {
				t.Errorf("%s (nav=%v): Content-Disposition=%q, want attachment", path, nav, got)
			}

			if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
				t.Errorf("%s (nav=%v): CSP=%q lacks sandbox", path, nav, csp)
			}
		}
	}

	if got := serve("/v2x/other", "text/html", false).Header().Get("Content-Disposition"); got != "" {
		t.Errorf("/v2x/ is not the registry API, got Content-Disposition %q", got)
	}
}
