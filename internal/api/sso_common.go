// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ssoErrorCodes are the failure codes the SSO login, callback and ACS
// endpoints hand to the login page as /login?sso_error=<code>. The browser
// follows these endpoints by navigation, so a failure is a redirect the SPA
// shows in the user's language, not a JSON body. The set is closed: the value
// in the URL is never attacker-controlled text. web/src/lib/ssoErrors.ts holds
// the message of each code (TestSSOErrorCodesMatchWeb keeps both in step).
//
//nolint:gochecknoglobals // immutable lookup table
var ssoErrorCodes = []string{
	// An identity-level conflict (email owned by another account, identity
	// bound elsewhere) or a policy rejection. Deliberately the same for every
	// such case so the page does not reveal whether a local account exists;
	// the cause is in the server log.
	"sso_login_denied",
	"user_not_provisioned",
	"account_disabled",
	"provider_not_found",
	"oidc_discovery_failed",
	"idp_metadata_unavailable",
	"id_token_missing",
	"id_token_expired",
	"exchange_failed",
	"missing_session_cookies",
	"state_mismatch",
	"nonce_mismatch",
	"saml_signature_invalid",
	"saml_audience_mismatch",
	"saml_assertion_expired",
	"saml_invalid",
	"missing_subject",
	"invalid_relay_state",
	// The IdP answered the authorization request with an error: access_denied
	// when the user or the IdP refused, oidc_error for every other value.
	"access_denied",
	"oidc_error",
	"internal_error",
}

// redirectSSOError sends the browser to the login page with the failure code
// (see ssoErrorCodes). 303 so a POST (SAML ACS) is not replayed. A code outside
// the set is reported as internal_error.
func redirectSSOError(w http.ResponseWriter, r *http.Request, code string) {
	if !slices.Contains(ssoErrorCodes, code) {
		code = "internal_error"
	}

	http.Redirect(w, r, "/login?"+url.Values{"sso_error": {code}}.Encode(), http.StatusSeeOther)
}

// oidcProviderErrorCode maps the `error` the IdP put on the callback onto a
// code of ssoErrorCodes. The value is attacker-controlled (anyone can send the
// browser to the callback URL), so it is never echoed: only access_denied
// (RFC 6749 section 4.1.2.1) is kept and everything else is oidc_error.
func oidcProviderErrorCode(idpError string) string {
	if idpError == "access_denied" {
		return "access_denied"
	}

	return "oidc_error"
}

// writeSSOProvisionError maps a provisioning failure onto the login-page
// redirect shared by every SSO flow.
func writeSSOProvisionError(w http.ResponseWriter, r *http.Request, log *logger.Logger, flow, slug string, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotProvisioned):
		redirectSSOError(w, r, "user_not_provisioned")
	case errors.Is(err, auth.ErrAccountDisabled):
		redirectSSOError(w, r, "account_disabled")
	case errors.Is(err, auth.ErrEmailAlreadyInUse), errors.Is(err, auth.ErrIdentityConflict):
		log.Warnw(flow+": sso login denied", "slug", slug, "reason", err.Error())
		redirectSSOError(w, r, "sso_login_denied")
	default:
		log.Errorw(flow+": provision", "slug", slug, "error", err)
		redirectSSOError(w, r, "internal_error")
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

// establishSSOSessionStore is the part of the database establishSSOSession needs.
type establishSSOSessionStore interface {
	CreateSession(ctx context.Context, session *models.Session) (*models.Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	SetSessionSSO(ctx context.Context, sessionID string, providerSlug string, encryptedIDToken string) error
}

// establishSSOSession rotates the browser session: any session the request
// already carries is revoked before a fresh one (new random token, new CSRF
// token) is created, so an identifier planted or captured before login never
// becomes an authenticated session. It writes the session and CSRF cookies and
// reports whether it succeeded; on failure it has redirected to the login page.
func establishSSOSession(
	w http.ResponseWriter, r *http.Request, db establishSSOSessionStore, log *logger.Logger,
	baseURL string, trusted *middleware.TrustedProxies, p ssoSessionParams,
) bool {
	ctx := r.Context()

	if old := sessionCookieValue(r); old != "" {
		if err := auth.DeleteSession(ctx, db, old); err != nil {
			log.Warnw("sso login: revoke pre-login session", "error", err)
		}
	}

	cookieValue, session, err := auth.CreateSession(ctx, db, p.UserID, middleware.ClientIP(r, trusted), r.UserAgent())
	if err != nil {
		log.Errorw("sso login: create session", "error", err)
		redirectSSOError(w, r, "internal_error")

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
	setCSRFCookie(w, secure, cookieValue)

	return true
}
