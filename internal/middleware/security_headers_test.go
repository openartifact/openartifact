// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
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
