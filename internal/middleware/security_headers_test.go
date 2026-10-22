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
