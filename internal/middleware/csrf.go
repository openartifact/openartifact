// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/auth"
)

const CSRFTokenCookieName = "oa_csrf"

func NewCSRFToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(token), nil
}

// usesExplicitCredential reports whether RBAC authenticated the request with a
// header credential rather than the ambient session cookie.
func usesExplicitCredential(r *http.Request) bool {
	p := PrincipalFromContext(r.Context())
	if p == nil {
		return false
	}

	switch p.Credential {
	case auth.CredentialBearerToken, auth.CredentialBasicToken, auth.CredentialLegacy:
		return true
	}

	return false
}

// crossSiteBasic reports whether r is Basic-authenticated and carries browser
// signals showing it was initiated from another site. Browsers cache Basic
// credentials per origin and attach them to cross-site requests; CLI clients
// (curl, mvn, npm, docker) send neither Origin nor Sec-Fetch-Site.
func crossSiteBasic(r *http.Request) bool {
	p := PrincipalFromContext(r.Context())
	if p == nil || p.Credential != auth.CredentialBasicToken {
		return false
	}

	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}

	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return true
		}
	}

	return false
}

// CSRFMiddleware enforces the double-submit CSRF token. baseURL is the public
// base URL: an https value forces Secure on the seeded cookie.
func CSRFMiddleware(baseURL string) func(http.Handler) http.Handler {
	httpsBase := strings.HasPrefix(strings.ToLower(baseURL), "https://")

	return func(next http.Handler) http.Handler {
		return csrfHandler(next, httpsBase)
	}
}

func csrfHandler(next http.Handler, httpsBase bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			if cookie, err := r.Cookie(CSRFTokenCookieName); err != nil || cookie.Value == "" {
				if token, err := NewCSRFToken(); err == nil {
					http.SetCookie(w, &http.Cookie{ //nolint:gosec // CSRF double-submit cookie must be readable by JS, so HttpOnly is deliberately unset
						Name:     CSRFTokenCookieName,
						Value:    token,
						Path:     "/",
						Secure:   httpsBase || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
						SameSite: http.SameSiteLaxMode,
					})
				}
			}

			next.ServeHTTP(w, r)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/sso/saml/") && strings.HasSuffix(r.URL.Path, "/acs") {
			next.ServeHTTP(w, r)
			return
		}

		// CSRF abuses credentials the browser attaches on its own (the session
		// cookie). A request authenticated by an explicit header credential
		// (bearer/basic token, legacy key) cannot be forged cross-site, and CI
		// clients have no CSRF cookie to echo back.
		// Browser-cached Basic credentials ARE attached ambiently, though, so a
		// Basic request that is visibly cross-site is rejected below.
		if usesExplicitCredential(r) && !crossSiteBasic(r) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(CSRFTokenCookieName)
		if err != nil || cookie.Value == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.Header.Get("X-Csrf-Token"))) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "csrf_protection",
				"message": "Missing or invalid CSRF token",
			})

			return
		}

		next.ServeHTTP(w, r)
	})
}
