// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
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
func setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Lax are set; Secure is derived from the request scheme so plain-HTTP dev setups still work
		Name:     auth.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.SessionDuration.Seconds()),
	})
}

// clearSessionCookie expires the session cookie using the same attributes it
// was set with, so browsers match and drop it.
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Lax are set; Secure is derived from the request scheme so plain-HTTP dev setups still work
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func setCSRFCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // CSRF double-submit cookie must be readable by JS, so HttpOnly is deliberately unset
		Name:     middleware.CSRFTokenCookieName,
		Value:    token,
		Path:     "/",
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.SessionDuration.Seconds()),
	})
}

func clearCSRFCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // CSRF double-submit cookie must be readable by JS, so HttpOnly is deliberately unset
		Name:     middleware.CSRFTokenCookieName,
		Value:    "",
		Path:     "/",
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
