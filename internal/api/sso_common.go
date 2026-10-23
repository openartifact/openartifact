// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ssoLoginDeniedMessage is what a client sees when an identity-level conflict
// (email owned by another account, identity bound elsewhere) stops an SSO
// login. It is deliberately the same for every such case so the response does
// not reveal whether a local account exists; the cause is in the server log.
const ssoLoginDeniedMessage = "SSO sign-in could not be completed; contact your administrator"

// writeSSOProvisionError maps a provisioning failure onto the HTTP response
// shared by every SSO flow.
func writeSSOProvisionError(w http.ResponseWriter, log *logger.Logger, flow, slug string, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotProvisioned):
		writeError(w, http.StatusForbidden, "user_not_provisioned", "auto-provisioning is disabled for this provider")
	case errors.Is(err, auth.ErrAccountDisabled):
		writeError(w, http.StatusForbidden, "account_disabled", "user account is disabled")
	case errors.Is(err, auth.ErrEmailAlreadyInUse), errors.Is(err, auth.ErrIdentityConflict):
		log.Warnw(flow+": sso login denied", "slug", slug, "reason", err.Error())
		writeError(w, http.StatusForbidden, "sso_login_denied", ssoLoginDeniedMessage)
	case errors.Is(err, auth.ErrEmailLinkAmbiguous):
		log.Errorw(flow+": sso login denied", "slug", slug, "reason", err.Error())
		writeError(w, http.StatusForbidden, "sso_login_denied", ssoLoginDeniedMessage)
	default:
		log.Errorw(flow+": provision", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "user provisioning failed")
	}
}

// ssoSessionParams describes the session an SSO login establishes.
type ssoSessionParams struct {
	UserID       string
	ProviderSlug string
	// IDToken is the raw OIDC ID token kept (encrypted) for RP-initiated
	// logout; empty for SAML and plain OAuth 2.0 logins.
	IDToken string
}

// establishSSOSession rotates the browser session: any session the request
// already carries is revoked before a fresh one (new random token, new CSRF
// token) is created, so an identifier planted or captured before login never
// becomes an authenticated session. It writes the session and CSRF cookies and
// reports whether it succeeded; on failure it has written the error response.
func establishSSOSession(
	w http.ResponseWriter, r *http.Request, db database.Database, log *logger.Logger,
	baseURL string, trusted *middleware.TrustedProxies, p ssoSessionParams,
) bool {
	ctx := r.Context()

	csrfToken, err := middleware.NewCSRFToken()
	if err != nil {
		log.Errorw("sso login: generate CSRF token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")

		return false
	}

	if old := sessionCookieValue(r); old != "" {
		if err := auth.DeleteSession(ctx, db, old); err != nil {
			log.Warnw("sso login: revoke pre-login session", "error", err)
		}
	}

	cookieValue, session, err := auth.CreateSession(ctx, db, p.UserID, middleware.ClientIP(r, trusted), r.UserAgent())
	if err != nil {
		log.Errorw("sso login: create session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")

		return false
	}

	if p.IDToken != "" {
		if err := auth.AttachSSOSession(ctx, db, session.ID, p.ProviderSlug, p.IDToken); err != nil {
			// Logout then falls back to a local-only sign-out.
			log.Warnw("sso login: remember id token for logout", "slug", p.ProviderSlug, "error", err)
		}
	}

	secure := isSecureContext(r, baseURL)
	setSessionCookie(w, secure, cookieValue)
	setCSRFCookie(w, secure, csrfToken)

	return true
}
