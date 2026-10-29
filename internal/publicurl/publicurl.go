// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package publicurl resolves the externally visible origin (scheme, host and
// optional path prefix) the server hands to clients inside generated documents:
// npm tarballs, the Cargo config.json, the NuGet service index, Composer dists,
// presigned upload relays.
//
// The configured BASE_URL is an explicit override and always wins. Without one
// the origin is derived from the request: the Host header and TLS state, or
// X-Forwarded-Host / X-Forwarded-Proto when the TCP peer is a configured trusted
// proxy (server.trustedProxies, the list ClientIP uses). Forwarded headers from
// any other peer are ignored, so a client cannot steer the URLs another client
// is given.
//
// Documents whose client resolves relative references (Helm, PyPI, the Terraform
// download URLs) do not use this package at all.
package publicurl

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type ctxKey struct{}

// varyHeaders are the request headers a derived origin depends on.
const varyHeaders = "Host, X-Forwarded-Host, X-Forwarded-Proto"

// Middleware derives the request origin once and stores it on the request
// context for Resolve and FromContext. explicit is BASE_URL when it was set
// (the middleware is then a no-op); fallback is the origin used when the
// request carries no usable Host. trusted reports whether the request's TCP
// peer is a trusted reverse proxy; nil trusts nobody.
func Middleware(explicit, fallback string, trusted func(*http.Request) bool) func(http.Handler) http.Handler {
	explicit = strings.TrimRight(strings.TrimSpace(explicit), "/")
	fallback = strings.TrimRight(strings.TrimSpace(fallback), "/")

	return func(next http.Handler) http.Handler {
		if explicit != "" {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := derive(r, trusted)
			if origin == "" {
				origin = fallback
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, origin)))
		})
	}
}

// Resolve returns the origin (scheme://host, plus the path of BASE_URL when it
// has one; never a trailing slash) clients should use for r. explicit is the
// configured BASE_URL, "" when the operator did not set one. When the origin is
// derived from the request the response is marked as varying on the headers it
// came from, so a shared cache never serves it to another origin; call Resolve
// before the response headers are written.
func Resolve(w http.ResponseWriter, r *http.Request, explicit string) string {
	if explicit = strings.TrimRight(explicit, "/"); explicit != "" {
		return explicit
	}

	w.Header().Add("Vary", varyHeaders)

	if v, ok := r.Context().Value(ctxKey{}).(string); ok {
		return v
	}

	// No middleware ran (a handler used on its own): the request's own Host,
	// trusting no forwarded header.
	return derive(r, nil)
}

// FromContext is Resolve for code that has only a context (storage presigning,
// MCP tools) and writes no HTTP response. explicit is BASE_URL, "" when unset;
// "" is returned when neither exists.
func FromContext(ctx context.Context, explicit string) string {
	if explicit = strings.TrimRight(explicit, "/"); explicit != "" {
		return explicit
	}

	v, _ := ctx.Value(ctxKey{}).(string)

	return v
}

// PathPrefix returns the path component of an origin ("/artifacts" for
// "https://host/artifacts", "" when there is none).
func PathPrefix(origin string) string {
	_, rest, ok := strings.Cut(origin, "://")
	if !ok {
		return ""
	}

	_, path, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}

	if path = strings.Trim(path, "/"); path == "" {
		return ""
	}

	return "/" + path
}

// derive builds scheme://host from r, or returns "" when no valid host exists.
func derive(r *http.Request, trusted func(*http.Request) bool) string {
	fromProxy := trusted != nil && trusted(r)

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if fromProxy {
		if p := firstToken(r.Header.Get("X-Forwarded-Proto")); p == "https" || p == "http" {
			scheme = p
		}
	}

	host := r.Host
	if fromProxy {
		if h := firstToken(r.Header.Get("X-Forwarded-Host")); h != "" {
			host = h
		}
	}

	if !validHost(host) {
		return ""
	}

	return scheme + "://" + strings.ToLower(host)
}

// firstToken returns the first comma-separated element of a forwarded header,
// the value the outermost proxy saw, lowercased and trimmed.
func firstToken(v string) string {
	first, _, _ := strings.Cut(v, ",")

	return strings.ToLower(strings.TrimSpace(first))
}

// validHost reports whether h is a plausible host[:port]: a DNS name, IPv4
// address or bracketed IPv6 address with an optional numeric port, and nothing
// else. Anything with a path, userinfo, whitespace or control character is
// refused, so a header value cannot smuggle extra URL components.
func validHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}

	host, port, err := net.SplitHostPort(h)
	if err != nil {
		host, port = h, ""
	}

	if err == nil && !allOf(port, isDigit) {
		return false
	}

	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return net.ParseIP(host[1:len(host)-1]) != nil
	}

	if err == nil && strings.Contains(h, "[") {
		return net.ParseIP(host) != nil
	}

	return host != "" && allOf(host, isHostChar)
}

func allOf(s string, ok func(byte) bool) bool {
	for i := range len(s) {
		if !ok(s[i]) {
			return false
		}
	}

	return s != ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHostChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || isDigit(c) || c == '-' || c == '.' || c == '_'
}
