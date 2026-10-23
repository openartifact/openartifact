// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type OIDCHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	hmacKey   []byte
	baseURL   string

	trustedProxies *middleware.TrustedProxies
}

func NewOIDCHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, hmacKey []byte, baseURL string) *OIDCHandler {
	return &OIDCHandler{db: db, log: log, evaluator: evaluator, hmacKey: hmacKey, baseURL: baseURL}
}

// WithTrustedProxies sets the reverse-proxy allowlist used to resolve the
// client IP recorded on new sessions. Without it the TCP peer address is used.
func (h *OIDCHandler) WithTrustedProxies(tp *middleware.TrustedProxies) *OIDCHandler {
	h.trustedProxies = tp
	return h
}

// ssoProviderUnavailable writes the 404 shared by the OIDC and SAML handlers
// for an unknown or disabled provider (indistinguishable to the caller) and
// reports whether it handled err.
func ssoProviderUnavailable(w http.ResponseWriter, err error) bool {
	if errors.Is(err, auth.ErrSSOProviderNotFound) || errors.Is(err, auth.ErrSSOProviderDisabled) {
		writeError(w, http.StatusNotFound, "provider_not_found", "SSO provider not found")
		return true
	}

	return false
}

func (h *OIDCHandler) RegisterPublic(public chi.Router) {
	public.Get("/api/v1/auth/sso/oidc/{slug}/login", h.handleLogin)
	public.Get("/api/v1/auth/sso/oidc/{slug}/callback", h.handleCallback)
	public.Post("/api/v1/auth/sso/oidc/{slug}/logout", h.handleLogout)
}

func (h *OIDCHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	provider, err := auth.LoadOIDCProvider(ctx, h.db, slug, h.baseURL)
	if err != nil {
		h.writeLoadError(w, "oidc login", slug, err)
		return
	}

	rawVerifier := make([]byte, 32)
	if _, err := rand.Read(rawVerifier); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate PKCE")
		return
	}

	codeVerifier := base64.RawURLEncoding.EncodeToString(rawVerifier)
	sum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(sum[:])

	rawState := make([]byte, 16)
	if _, err := rand.Read(rawState); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate state")
		return
	}

	state := hex.EncodeToString(rawState)

	setSSOStateCookie(w, r, h.baseURL, "oa_oidc_pkce_"+slug, codeVerifier)
	setSSOStateCookie(w, r, h.baseURL, "oa_oidc_state_"+slug, state)

	// The nonce binds the ID token to this browser; plain OAuth 2.0 providers
	// (github, oauth2) have no ID token.
	nonce := ""

	if !provider.IsOAuth2() {
		rawNonce := make([]byte, 16)
		if _, err := rand.Read(rawNonce); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate nonce")
			return
		}

		nonce = hex.EncodeToString(rawNonce)
		setSSOStateCookie(w, r, h.baseURL, "oa_oidc_nonce_"+slug, nonce)
	}

	if redirect := r.URL.Query().Get("redirect"); redirect != "" && isOIDCRelativePath(redirect) {
		setSSOStateCookie(w, r, h.baseURL, "oa_oidc_redirect_"+slug, redirect)
	}

	//nolint:gosec // G710: redirect URL is validated against configured provider URLs
	http.Redirect(w, r, provider.AuthURL(state, nonce, codeChallenge), http.StatusFound)
}

// writeLoadError maps a provider load failure onto the HTTP response shared
// by login and callback.
func (h *OIDCHandler) writeLoadError(w http.ResponseWriter, flow, slug string, err error) {
	if ssoProviderUnavailable(w, err) {
		return
	}

	if errors.Is(err, auth.ErrOIDCDiscoveryFailed) {
		writeError(w, http.StatusBadGateway, "oidc_discovery_failed", "OIDC discovery failed")
		return
	}

	h.log.Errorw(flow+": load provider", "slug", slug, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "failed to load provider")
}

// writeSSOLoginDenied answers a policy rejection (ErrSSOLoginDenied) with the
// generic 403 shared by every SSO flow; the reason stays in the server log.
func writeSSOLoginDenied(w http.ResponseWriter, log *logger.Logger, flow, slug string, err error) {
	log.Warnw(flow+": sso login denied", "slug", slug, "reason", err.Error())
	writeError(w, http.StatusForbidden, "sso_login_denied", ssoLoginDeniedMessage)
}

// writeExchangeError maps a code-exchange or ID token verification failure.
func (h *OIDCHandler) writeExchangeError(w http.ResponseWriter, slug string, err error) {
	switch {
	case ssoProviderUnavailable(w, err):
	case errors.Is(err, auth.ErrSSOLoginDenied):
		writeSSOLoginDenied(w, h.log, "oidc callback", slug, err)
	case errors.Is(err, auth.ErrOIDCDiscoveryFailed):
		writeError(w, http.StatusBadGateway, "oidc_discovery_failed", "OIDC discovery failed")
	case errors.Is(err, auth.ErrIDTokenMissing):
		writeError(w, http.StatusBadGateway, "id_token_missing", "ID token missing from response")
	case errors.Is(err, auth.ErrIDTokenExpired):
		writeError(w, http.StatusUnauthorized, "id_token_expired", "ID token has expired")
	default:
		h.log.Errorw("oidc callback: exchange", "slug", slug, "error", err)
		writeError(w, http.StatusBadGateway, "exchange_failed", "token exchange failed")
	}
}

// callbackCookies are the per-login browser state set by handleLogin.
type callbackCookies struct {
	pkce, state, nonce string
}

// readCallbackCookies returns the cookies the callback needs and reports
// whether all of them are present; on failure it has written the response.
// The nonce cookie is only required for providers that issue an ID token.
func readCallbackCookies(w http.ResponseWriter, r *http.Request, slug string, needNonce bool) (callbackCookies, bool) {
	var c callbackCookies

	for _, f := range []struct {
		name string
		dst  *string
		skip bool
	}{
		{"oa_oidc_pkce_" + slug, &c.pkce, false},
		{"oa_oidc_state_" + slug, &c.state, false},
		{"oa_oidc_nonce_" + slug, &c.nonce, !needNonce},
	} {
		if f.skip {
			continue
		}

		cookie, err := r.Cookie(f.name)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusBadRequest, "missing_session_cookies", "login session cookie missing")
			return callbackCookies{}, false
		}

		*f.dst = cookie.Value
	}

	return c, true
}

func (h *OIDCHandler) handleCallback(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	q := r.URL.Query()

	if oidcErr := q.Get("error"); oidcErr != "" {
		writeError(w, http.StatusBadRequest, "oidc_error", oidcErr)
		return
	}

	provider, err := auth.LoadOIDCProvider(ctx, h.db, slug, h.baseURL)
	if err != nil {
		h.writeLoadError(w, "oidc callback", slug, err)
		return
	}

	cookies, ok := readCallbackCookies(w, r, slug, !provider.IsOAuth2())
	if !ok {
		return
	}

	if !constTimeEqual(q.Get("state"), cookies.state) {
		writeError(w, http.StatusBadRequest, "state_mismatch", "state parameter does not match")
		return
	}

	res, err := auth.ExchangeWithRetry(ctx, h.db, slug, h.baseURL, q.Get("code"), cookies.pkce)
	if err != nil {
		h.writeExchangeError(w, slug, err)
		return
	}

	if res.IDToken != nil && !constTimeEqual(res.IDToken.Nonce, cookies.nonce) {
		writeError(w, http.StatusUnauthorized, "nonce_mismatch", "nonce does not match")
		return
	}

	ssoClaims, err := res.Provider.Identity(ctx, res, h.log)
	if err != nil {
		if errors.Is(err, auth.ErrSSOLoginDenied) {
			writeSSOLoginDenied(w, h.log, "oidc callback", slug, err)
			return
		}

		h.log.Errorw("oidc callback: resolve identity", "slug", slug, "error", err)
		writeError(w, http.StatusBadGateway, "exchange_failed", "failed to resolve the user identity")

		return
	}

	providerRow, err := h.db.GetSSOProviderBySlug(ctx, slug)
	if err != nil || providerRow == nil || !providerRow.Enabled {
		writeError(w, http.StatusNotFound, "provider_not_found", "SSO provider not found")
		return
	}

	user, err := auth.NewProvisioner(h.db, h.log).Provision(ctx, providerRow, ssoClaims)
	if err != nil {
		writeSSOProvisionError(w, h.log, "oidc callback", slug, err)
		return
	}

	if !establishSSOSession(w, r, h.db, h.log, h.baseURL, h.trustedProxies, ssoSessionParams{
		UserID: user.ID, ProviderSlug: slug, IDToken: res.RawIDToken,
	}) {
		return
	}

	for _, name := range []string{"oa_oidc_pkce_" + slug, "oa_oidc_state_" + slug, "oa_oidc_nonce_" + slug} {
		clearSSOStateCookie(w, r, h.baseURL, name)
	}

	redirect := "/"

	if rc, err := r.Cookie("oa_oidc_redirect_" + slug); err == nil {
		if isOIDCRelativePath(rc.Value) {
			redirect = rc.Value
		}

		clearSSOStateCookie(w, r, h.baseURL, "oa_oidc_redirect_"+slug)
	}

	//nolint:gosec // G710: redirect is a same-origin relative path (safeRedirectPath)
	http.Redirect(w, r, safeRedirectPath(redirect), http.StatusFound)
}

// handleLogout ends the local session and, when the session was created by
// this OIDC provider and the IdP supports RP-initiated logout, answers with
// the IdP's end_session URL. It sits outside the CSRF middleware, so it
// requires a same-origin request. The request body is ignored: the
// id_token_hint is the one stored with the session and the
// post_logout_redirect_uri is always this server's login page, so a caller can
// neither inject a hint nor steer the post-logout redirect.
func (h *OIDCHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) {
		return
	}

	completeLogout(w, r, h.db, h.log, h.baseURL, urlparams.Vars(r)["slug"])
}

// completeLogout deletes the request's session, clears the session cookies and
// answers 200 {"end_session_url"} when the session belongs to an OIDC provider
// (restricted to onlySlug when non-empty) whose IdP supports RP-initiated
// logout, else 204. Any failure to work out the IdP URL degrades to a plain
// local logout. The caller has already enforced same-origin.
func completeLogout(w http.ResponseWriter, r *http.Request, db database.Database, log *logger.Logger, baseURL, onlySlug string) {
	ctx := r.Context()
	endSessionURL := ""

	if value := sessionCookieValue(r); value != "" {
		// Looked up before the delete: the SSO record dies with the session.
		endSessionURL = rpLogoutURL(ctx, db, log, baseURL, value, onlySlug)

		if err := auth.DeleteSession(ctx, db, value); err != nil {
			log.Warnw("logout: delete session", "error", err)
		}
	}

	secure := isSecureContext(r, baseURL)
	clearSessionCookie(w, secure)
	clearCSRFCookie(w, secure)

	if endSessionURL != "" {
		writeJSON(w, http.StatusOK, map[string]string{"end_session_url": endSessionURL})
		return
	}

	writeNoContent(w)
}

// rpLogoutURL returns the IdP end_session URL for the session behind
// cookieValue, or "" when the session is not an OIDC SSO session (of onlySlug,
// when set) or the IdP has no end_session_endpoint.
func rpLogoutURL(ctx context.Context, db database.Database, log *logger.Logger, baseURL, cookieValue, onlySlug string) string {
	info, err := auth.LookupSessionSSO(ctx, db, cookieValue)
	if err != nil {
		log.Warnw("logout: read session sso record", "error", err)
		return ""
	}

	if info == nil || (onlySlug != "" && info.ProviderSlug != onlySlug) {
		return ""
	}

	provider, err := auth.LoadOIDCProvider(ctx, db, info.ProviderSlug, baseURL)
	if err != nil {
		log.Warnw("logout: load oidc provider", "slug", info.ProviderSlug, "error", err)
		return ""
	}

	endSessionURL, ok := provider.EndSession(info.IDToken, baseURL+"/login")
	if !ok {
		return ""
	}

	return endSessionURL
}

func isOIDCRelativePath(path string) bool {
	return safeRedirectPath(path) == path
}

func constTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}

	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}

	return diff == 0
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}

	return b
}
