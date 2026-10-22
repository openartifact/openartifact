// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
)

// isSecureRequest returns true when the request appears to be served over
// TLS — directly or through a reverse proxy that sets X-Forwarded-Proto.
//
// The Set-Cookie Secure attribute can only be honoured by the browser when
// the cookie is delivered over a secure context. Hard-coding Secure: true
// makes plain-HTTP development (e.g. http://localhost) silently drop the
// cookie, breaking session login.
//
// X-Forwarded-Proto can only ever make a cookie *more* restrictive (Secure),
// so a client forging it can at worst stop its own browser from sending the
// cookie back over plain HTTP; it cannot weaken anyone else's session.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")

	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// setSessionCookie issues the session cookie: HttpOnly (never readable by
// script), Secure whenever the request arrived over TLS, SameSite=Lax and
// scoped to the whole site.
func setSessionCookie(w http.ResponseWriter, secure bool, value string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Lax are set; Secure is derived from the request scheme or https base URL so plain-HTTP dev setups still work
		Name:     auth.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		// Absolute lifetime: the sliding idle expiry is enforced server-side.
		MaxAge: int(auth.SessionAbsoluteLifetime().Seconds()),
	})
}

// clearSessionCookie expires the session cookie using the same attributes it
// was set with, so browsers match and drop it.
func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Lax are set; Secure is derived from the request scheme or https base URL so plain-HTTP dev setups still work
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func setCSRFCookie(w http.ResponseWriter, secure bool, token string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // CSRF double-submit cookie must be readable by JS, so HttpOnly is deliberately unset
		Name:     middleware.CSRFTokenCookieName,
		Value:    token,
		Path:     "/",
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.SessionAbsoluteLifetime().Seconds()),
	})
}

func clearCSRFCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // CSRF double-submit cookie must be readable by JS, so HttpOnly is deliberately unset
		Name:     middleware.CSRFTokenCookieName,
		Value:    "",
		Path:     "/",
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// isSecureContext reports whether cookies issued for r must carry Secure: the
// request arrived over TLS (directly or via X-Forwarded-Proto) or the
// configured public base URL is https.
func isSecureContext(r *http.Request, baseURL string) bool {
	return isSecureRequest(r) || strings.HasPrefix(strings.ToLower(baseURL), "https://")
}

// setSSOStateCookie issues a short-lived, HttpOnly, SameSite=Lax cookie used to
// carry SSO flow state (PKCE verifier, state, nonce, post-login redirect).
func setSSOStateCookie(w http.ResponseWriter, r *http.Request, baseURL, name, value string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Lax are set; Secure follows TLS / https base URL so plain-HTTP dev setups still work
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureContext(r, baseURL),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
}

// clearSSOStateCookie expires a cookie set by setSSOStateCookie.
func clearSSOStateCookie(w http.ResponseWriter, r *http.Request, baseURL, name string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Lax are set; Secure follows TLS / https base URL so plain-HTTP dev setups still work
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureContext(r, baseURL),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// safeRedirectPath returns p when it is a same-origin relative path and "/"
// otherwise. It rejects absolute and scheme-relative URLs ("//host", "/\host",
// "https://host"), backslashes and control characters, so the value can be
// handed to http.Redirect without becoming an open redirect.
func safeRedirectPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}

	for _, c := range p {
		if c == '\\' || c < 0x20 || c == 0x7f {
			return "/"
		}
	}

	u, err := url.Parse(p)
	if err != nil || u.IsAbs() || u.Hostname() != "" {
		return "/"
	}

	// Rebuild from path components only, so nothing host-bearing survives.
	return (&url.URL{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery, Fragment: u.Fragment}).String()
}

// requireSameOrigin rejects browser-initiated cross-site requests to the
// unauthenticated session endpoints (login CSRF). Non-browser clients send
// neither Sec-Fetch-Site nor Origin and pass.
func requireSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	cross := r.Header.Get("Sec-Fetch-Site") == "cross-site"

	if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
		u, err := url.Parse(origin)
		cross = cross || err != nil || !strings.EqualFold(u.Host, r.Host)
	} else if origin == "null" {
		cross = true
	}

	if cross {
		writeError(w, http.StatusForbidden, "csrf_protection", "cross-site request rejected")
		return false
	}

	return true
}

// requireJSONSameOrigin additionally demands a JSON content type, which a
// cross-site HTML form cannot produce without a CORS preflight.
func requireJSONSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if !requireSameOrigin(w, r) {
		return false
	}

	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}

	return true
}
