// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/httpx"
)

// Shared, read-only header values (see SecurityHeadersMiddleware).
//
//nolint:gochecknoglobals // immutable constants
var (
	nosniffValue     = []string{"nosniff"}
	frameDenyValue   = []string{"DENY"}
	noReferrerValue  = []string{"no-referrer"}
	baselineCSPValue = []string{"frame-ancestors 'none'"}
	artifactCSPValue = []string{"sandbox; default-src 'none'; frame-ancestors 'none'"}
	hstsValue        = []string{"max-age=31536000; includeSubDomains"}
)

// SecurityHeadersMiddleware sets baseline hardening headers on every response.
// hsts enables Strict-Transport-Security and should only be set when the
// server is reachable over HTTPS. Existing handler-set values are preserved
// (headers are added before the handler runs, handlers may override).
func SecurityHeadersMiddleware(hsts bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// The values are constants: sharing one single-element slice per
			// header instead of allocating a fresh one per request. The slices
			// have len == cap, so Header.Add appends into a copy and Header.Set
			// replaces them; nothing may modify them in place.
			h["X-Content-Type-Options"] = nosniffValue
			h["X-Frame-Options"] = frameDenyValue
			h["Referrer-Policy"] = noReferrerValue
			h["Content-Security-Policy"] = baselineCSPValue
			// Browser page loads of SPA routes under /repos/ are answered by the
			// SPA (see RBACMiddleware), not by an artifact handler: they need
			// scripts and must render, so they keep the baseline CSP.
			if isArtifactPath(r) {
				// Artifact bytes are uploader-controlled and served from the
				// app origin: sandbox them (opaque origin, no scripts, no
				// cookies/API access) and force a download for any type a
				// browser would render actively.
				h["Content-Security-Policy"] = artifactCSPValue
				w = &artifactWriter{ResponseWriter: w}
			}
			if hsts {
				h["Strict-Transport-Security"] = hstsValue
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isArtifactPath reports whether r addresses artifact bytes: everything under
// /repos/ except SPA page loads, and the OCI registry API under /v2/, which has
// no SPA pages and serves manifests and listings whose types and bodies come
// from uploaders or proxied upstreams.
func isArtifactPath(r *http.Request) bool {
	p := r.URL.Path
	if p == "/v2" || strings.HasPrefix(p, "/v2/") {
		return true
	}

	return strings.HasPrefix(p, "/repos/") && !isSPANavigation(r)
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
	a.commit()

	return a.ResponseWriter.Write(b) //nolint:wrapcheck // transparent writer wrapper
}

// commit writes the implicit 200 of a handler that sent a body without
// WriteHeader, after deciding the disposition net/http would otherwise commit
// together with a sniffed type.
func (a *artifactWriter) commit() {
	if a.wroteHeader {
		return
	}

	if a.Header().Get("Content-Type") == "" {
		a.Header().Set("Content-Disposition", "attachment")
	}

	a.WriteHeader(http.StatusOK)
}

// ReadFrom keeps sendfile(2) reachable for artifact downloads: the response
// writer underneath serves a file through its own ReadFrom, which a wrapper
// that only embeds http.ResponseWriter hides.
func (a *artifactWriter) ReadFrom(r io.Reader) (int64, error) {
	a.commit()

	return httpx.ReadFrom(a.ResponseWriter, r) //nolint:wrapcheck // transparent writer wrapper
}

// Flush forwards to the underlying writer when it can flush.
func (a *artifactWriter) Flush() { httpx.Flush(a.ResponseWriter) }

func (a *artifactWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }
