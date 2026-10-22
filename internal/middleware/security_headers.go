// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"mime"
	"net/http"
	"strings"
)

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
			if strings.HasPrefix(r.URL.Path, "/repos/") {
				// Artifact bytes are uploader-controlled and served from the
				// app origin: sandbox them (opaque origin, no scripts, no
				// cookies/API access) and force a download for any type a
				// browser would render actively.
				h.Set("Content-Security-Policy", "sandbox; default-src 'none'; frame-ancestors 'none'")
				w = &artifactWriter{ResponseWriter: w}
			}
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// inlineSafeTypes are the media types a browser displays without executing
// anything. Every other type (html, xhtml, svg, xml, javascript, json, ...)
// is served as an attachment.
//
//nolint:gochecknoglobals // read-only allowlist
var inlineSafeTypes = map[string]bool{
	"text/plain":               true,
	"application/pdf":          true,
	"application/octet-stream": true,
	"image/png":                true,
	"image/jpeg":               true,
	"image/gif":                true,
	"image/webp":               true,
	"image/avif":               true,
	"image/bmp":                true,
}

// inlineSafe reports whether contentType may be rendered inline. A missing
// type is unsafe: the browser would sniff it.
func inlineSafe(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	return inlineSafeTypes[mt]
}

// artifactWriter adds Content-Disposition: attachment to successful artifact
// responses whose Content-Type is not on the inline allowlist.
type artifactWriter struct {
	http.ResponseWriter

	wroteHeader bool
}

func (a *artifactWriter) WriteHeader(code int) {
	if a.wroteHeader {
		return
	}

	a.wroteHeader = true

	h := a.Header()
	if code >= 200 && code < 300 && h.Get("Content-Disposition") == "" && !inlineSafe(h.Get("Content-Type")) {
		h.Set("Content-Disposition", "attachment")
	}

	a.ResponseWriter.WriteHeader(code)
}

func (a *artifactWriter) Write(b []byte) (int, error) {
	if !a.wroteHeader {
		// Decide the disposition before net/http sniffs and commits headers.
		if a.Header().Get("Content-Type") == "" {
			a.Header().Set("Content-Disposition", "attachment")
		}

		a.WriteHeader(http.StatusOK)
	}

	return a.ResponseWriter.Write(b)
}

func (a *artifactWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }
