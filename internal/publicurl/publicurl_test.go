// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package publicurl_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/openartifact/openartifact/internal/publicurl"
)

func trustAll(*http.Request) bool { return true }

func trustNone(*http.Request) bool { return false }

func resolved(t *testing.T, explicit string, trusted func(*http.Request) bool, mutate func(*http.Request)) (string, http.Header) {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/r/x", http.NoBody)
	mutate(r)

	var (
		got string
		hdr http.Header
	)

	h := publicurl.Middleware(explicit, "http://fallback.test", trusted)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = publicurl.Resolve(w, r, explicit)
		hdr = w.Header()
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)

	return got, hdr
}

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		explicit string
		trusted  func(*http.Request) bool
		mutate   func(*http.Request)
		want     string
		varies   bool
	}{
		{"explicit wins over headers", "https://oa.example.com/", trustAll, func(r *http.Request) {
			r.Host = "other.test"
			r.Header.Set("X-Forwarded-Host", "evil.test")
		}, "https://oa.example.com", false},
		{
			"explicit keeps its path prefix",
			"https://oa.example.com/artifacts",
			trustNone,
			func(*http.Request) {},
			"https://oa.example.com/artifacts",
			false,
		},
		{"host header", "", trustNone, func(r *http.Request) { r.Host = "127.0.0.1:18080" }, "http://127.0.0.1:18080", true},
		{"host is lowercased", "", trustNone, func(r *http.Request) { r.Host = "OA.Example.COM" }, "http://oa.example.com", true},
		{
			"direct TLS",
			"",
			trustNone,
			func(r *http.Request) { r.Host = "oa.test"; r.TLS = &tls.ConnectionState{} },
			"https://oa.test",
			true,
		},
		{"trusted proxy forwards proto and host", "", trustAll, func(r *http.Request) {
			r.Host = "oa:8080"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-Host", "artifacts.example.com")
		}, "https://artifacts.example.com", true},
		{"first forwarded value wins", "", trustAll, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Proto", "https, http")
			r.Header.Set("X-Forwarded-Host", "a.example.com, b.internal")
		}, "https://a.example.com", true},
		{"untrusted peer cannot forward", "", trustNone, func(r *http.Request) {
			r.Host = "real.test"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-Host", "evil.test")
		}, "http://real.test", true},
		{"forwarded proto is restricted to http and https", "", trustAll, func(r *http.Request) {
			r.Host = "oa.test"
			r.Header.Set("X-Forwarded-Proto", "javascript")
		}, "http://oa.test", true},
		{"ipv6 literal with port", "", trustNone, func(r *http.Request) { r.Host = "[::1]:8080" }, "http://[::1]:8080", true},
		{"hostile host falls back", "", trustNone, func(r *http.Request) { r.Host = "a.test/evil" }, "http://fallback.test", true},
		{"userinfo in forwarded host is refused", "", trustAll, func(r *http.Request) {
			r.Host = "oa.test"
			r.Header.Set("X-Forwarded-Host", "user@evil.test")
		}, "http://fallback.test", true},
		{"empty port is refused", "", trustNone, func(r *http.Request) { r.Host = "oa.test:" }, "http://fallback.test", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, hdr := resolved(t, tt.explicit, tt.trusted, tt.mutate)
			assert.Equal(t, tt.want, got)

			if tt.varies {
				assert.Contains(t, hdr.Get("Vary"), "X-Forwarded-Host")
			} else {
				assert.Empty(t, hdr.Values("Vary"))
			}
		})
	}
}

func TestResolveWithoutMiddlewareTrustsNoHeader(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", http.NoBody)
	r.Host = "direct.test"
	r.Header.Set("X-Forwarded-Host", "evil.test")

	assert.Equal(t, "http://direct.test", publicurl.Resolve(httptest.NewRecorder(), r, ""))
}

func TestFromContextAndPathPrefix(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", http.NoBody)
	assert.Equal(t, "https://set.test", publicurl.FromContext(r.Context(), "https://set.test/"))
	assert.Empty(t, publicurl.FromContext(r.Context(), ""))

	assert.Equal(t, "/artifacts", publicurl.PathPrefix("https://host/artifacts/"))
	assert.Empty(t, publicurl.PathPrefix("https://host"))
	assert.Empty(t, publicurl.PathPrefix(""))
}
