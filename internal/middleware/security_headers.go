// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import "net/http"

// SecurityHeadersMiddleware sets baseline hardening headers on every response.
// hsts enables Strict-Transport-Security and should only be set when the
// server is reachable over HTTPS. Existing handler-set values are preserved
// (headers are added before the handler runs, handlers may override).
func SecurityHeadersMiddleware(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "frame-ancestors 'none'")
			h.Set("Referrer-Policy", "no-referrer")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
