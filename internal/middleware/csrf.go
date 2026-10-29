// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/publicurl"
)

const CSRFTokenCookieName = "oa_csrf"

// csrfHeaderName is the request header that echoes the CSRF cookie.
const csrfHeaderName = "X-Csrf-Token"

// csrfTokenLabel domain-separates the CSRF token from any other use of the
// session secret.
const csrfTokenLabel = "openartifact csrf token v1"

// newCSRFToken returns a random token for requests that carry no session.
func newCSRFToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(token), nil
}

// CSRFTokenForSession derives the CSRF token of a browser session from its
// session cookie value: HMAC-SHA256 keyed with that (256-bit random, HttpOnly)
// secret. Only a holder of the session cookie can compute it, so a token
// planted from a sibling subdomain, or kept from an earlier session, never
// validates for another session. The session cookie value is rotated on every
// login, which rotates the token with it.
func CSRFTokenForSession(sessionCookieValue string) string {
	mac := hmac.New(sha256.New, []byte(sessionCookieValue))
	_, _ = mac.Write([]byte(csrfTokenLabel))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SessionCSRFCookie is the readable (not HttpOnly) cookie that hands the CSRF
// token of the session sessionCookieValue to the SPA, which echoes it in
// X-CSRF-Token.
func SessionCSRFCookie(sessionCookieValue string, secure bool) *http.Cookie {
	return csrfCookie(CSRFTokenForSession(sessionCookieValue), secure, int(auth.SessionAbsoluteLifetime().Seconds()))
}

func csrfCookie(value string, secure bool, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // CSRF cookie must be readable by JS, so HttpOnly is deliberately unset
		Name:     CSRFTokenCookieName,
		Value:    value,
		Path:     "/",
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
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

// sessionCookieOf returns the session cookie value when RBAC authenticated the
// request with it, "" otherwise (anonymous or header credential).
func sessionCookieOf(r *http.Request) string {
	p := PrincipalFromContext(r.Context())
	if p == nil || p.Credential != auth.CredentialSession {
		return ""
	}

	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}

	return c.Value
}

// csrfGuard holds the deployment facts the CSRF checks depend on.
type csrfGuard struct {
	httpsBase  bool
	baseOrigin string
	trusted    *TrustedProxies
}

// secure reports whether the CSRF cookie must carry Secure: the configured
// public base URL is https, or the request reached the server over TLS
// (directly, or through a trusted proxy that says so in X-Forwarded-Proto). The
// header of any other peer is ignored.
func (g *csrfGuard) secure(r *http.Request) bool {
	return g.httpsBase || publicurl.Scheme(r, g.trusted.TrustsPeer) == "https"
}

// crossSiteBasic reports whether r is Basic-authenticated and carries browser
// signals showing it was initiated from another site. Browsers cache Basic
// credentials per origin and attach them to cross-site requests; CLI clients
// (curl, mvn, npm, docker) send neither Origin nor Sec-Fetch-Site. The Origin
// is compared with the origin the client reached (trusted-proxy rules of
// internal/publicurl) and with the configured base URL.
func (g *csrfGuard) crossSiteBasic(r *http.Request) bool {
	p := PrincipalFromContext(r.Context())
	if p == nil || p.Credential != auth.CredentialBasicToken {
		return false
	}

	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}

	origin := strings.ToLower(strings.TrimSpace(r.Header.Get("Origin")))
	if origin == "" {
		return false
	}

	if origin == publicurl.Origin(r, g.trusted.TrustsPeer) {
		return false
	}

	return g.baseOrigin == "" || origin != g.baseOrigin
}

// CSRFMiddleware enforces CSRF protection for browser sessions. baseURL is the
// public base URL: an https value forces Secure on the CSRF cookie and its
// origin is accepted as the origin of Basic-authenticated browser requests.
// trusted lists the reverse proxies whose X-Forwarded-Proto / X-Forwarded-Host
// may be believed (nil trusts nobody).
//
// A state-changing request authenticated by the session cookie must echo the
// token bound to that session in X-CSRF-Token, on every path (the API, /repos,
// /v2, ...). Requests authenticated by an explicit header credential cannot be
// forged cross-site and are unaffected, except Basic requests that are visibly
// cross-site on /api/v1.
func CSRFMiddleware(baseURL string, trusted *TrustedProxies) func(http.Handler) http.Handler {
	g := &csrfGuard{
		httpsBase: strings.HasPrefix(strings.ToLower(baseURL), "https://"),
		trusted:   trusted,
	}

	if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
		g.baseOrigin = strings.ToLower(u.Scheme + "://" + u.Host)
	}

	return func(next http.Handler) http.Handler {
		return g.handler(next)
	}
}

// isCSRFSafeMethod reports whether method cannot change state: everything else
// (including WebDAV-style verbs) is guarded.
func isCSRFSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	return false
}

func (g *csrfGuard) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inAPI := strings.HasPrefix(r.URL.Path, "/api/v1/")
		session := sessionCookieOf(r)

		if isCSRFSafeMethod(r.Method) {
			if inAPI {
				g.seedCookie(w, r, session)
			}

			next.ServeHTTP(w, r)

			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/sso/saml/") && strings.HasSuffix(r.URL.Path, "/acs") {
			next.ServeHTTP(w, r)
			return
		}

		if session != "" {
			if !tokensEqual(r.Header.Get(csrfHeaderName), CSRFTokenForSession(session)) {
				writeCSRFError(w)
				return
			}

			next.ServeHTTP(w, r)

			return
		}

		// No ambient session credential. Outside the API nothing is forgeable
		// cross-site: a header credential or an anonymous request carries no
		// browser-held authority.
		if !inAPI {
			next.ServeHTTP(w, r)
			return
		}

		// A request authenticated by an explicit header credential (bearer/basic
		// token, legacy key) cannot be forged cross-site, and CI clients have no
		// CSRF cookie to echo back. Browser-cached Basic credentials ARE attached
		// ambiently, though, so a Basic request that is visibly cross-site falls
		// through to the double-submit check below.
		if usesExplicitCredential(r) && !g.crossSiteBasic(r) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(CSRFTokenCookieName)
		if err != nil || cookie.Value == "" || !tokensEqual(cookie.Value, r.Header.Get(csrfHeaderName)) {
			writeCSRFError(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// seedCookie makes sure the browser holds the CSRF cookie the SPA reads. For a
// session it is the token bound to that session, replaced when the cookie holds
// anything else (a pre-binding random value, a token planted by a sibling
// subdomain, the token of an earlier session). Without a session a random
// token is issued once.
func (g *csrfGuard) seedCookie(w http.ResponseWriter, r *http.Request, session string) {
	current, err := r.Cookie(CSRFTokenCookieName)
	have := err == nil && current.Value != ""

	if session != "" {
		want := CSRFTokenForSession(session)
		if !have || !tokensEqual(current.Value, want) {
			http.SetCookie(w, csrfCookie(want, g.secure(r), int(auth.SessionAbsoluteLifetime().Seconds())))
		}

		return
	}

	if have {
		return
	}

	if token, err := newCSRFToken(); err == nil {
		http.SetCookie(w, csrfCookie(token, g.secure(r), 0))
	}
}

func tokensEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeCSRFError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"csrf_protection","message":"Missing or invalid CSRF token"}` + "\n"))
}
