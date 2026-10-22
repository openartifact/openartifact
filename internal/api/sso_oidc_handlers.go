// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

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
		if ssoProviderUnavailable(w, err) {
			return
		}

		if errors.Is(err, auth.ErrOIDCDiscoveryFailed) {
			writeError(w, http.StatusBadGateway, "oidc_discovery_failed", "OIDC discovery failed")
			return
		}

		h.log.Errorw("oidc login: load provider", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load provider")

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

	rawNonce := make([]byte, 16)
	if _, err := rand.Read(rawNonce); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate nonce")
		return
	}

	nonce := hex.EncodeToString(rawNonce)

	setSSOStateCookie(w, r, h.baseURL, "oa_oidc_pkce_"+slug, codeVerifier)
	setSSOStateCookie(w, r, h.baseURL, "oa_oidc_state_"+slug, state)
	setSSOStateCookie(w, r, h.baseURL, "oa_oidc_nonce_"+slug, nonce)

	if redirect := r.URL.Query().Get("redirect"); redirect != "" && isOIDCRelativePath(redirect) {
		setSSOStateCookie(w, r, h.baseURL, "oa_oidc_redirect_"+slug, redirect)
	}

	//nolint:gosec // G710: redirect URL is validated against configured provider URLs
	http.Redirect(w, r, provider.AuthURL(state, nonce, codeChallenge), http.StatusFound)
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *OIDCHandler) handleCallback(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	q := r.URL.Query()

	if oidcErr := q.Get("error"); oidcErr != "" {
		writeError(w, http.StatusBadRequest, "oidc_error", oidcErr)
		return
	}

	pkceCookie, err := r.Cookie("oa_oidc_pkce_" + slug)
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_session_cookies", "PKCE cookie missing")
		return
	}

	stateCookie, err := r.Cookie("oa_oidc_state_" + slug)
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_session_cookies", "state cookie missing")
		return
	}

	nonceCookie, err := r.Cookie("oa_oidc_nonce_" + slug)
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_session_cookies", "nonce cookie missing")
		return
	}

	if stateCookie.Value == "" || !constTimeEqual(q.Get("state"), stateCookie.Value) {
		writeError(w, http.StatusBadRequest, "state_mismatch", "state parameter does not match")
		return
	}

	idToken, claimsMap, provider, err := auth.ExchangeWithRetry(ctx, h.db, slug, h.baseURL, q.Get("code"), pkceCookie.Value)
	if err != nil {
		if ssoProviderUnavailable(w, err) {
			return
		}

		if errors.Is(err, auth.ErrOIDCDiscoveryFailed) {
			writeError(w, http.StatusBadGateway, "oidc_discovery_failed", "OIDC discovery failed")
			return
		}

		if errors.Is(err, auth.ErrIDTokenMissing) {
			writeError(w, http.StatusBadGateway, "id_token_missing", "ID token missing from response")
			return
		}

		if errors.Is(err, auth.ErrIDTokenExpired) {
			writeError(w, http.StatusUnauthorized, "id_token_expired", "ID token has expired")
			return
		}

		h.log.Errorw("oidc callback: exchange", "slug", slug, "error", err)
		writeError(w, http.StatusBadGateway, "exchange_failed", "token exchange failed")

		return
	}

	var nonceClaims struct {
		Nonce string `json:"nonce"`
	}
	if err := idToken.Claims(&nonceClaims); err != nil || nonceClaims.Nonce != nonceCookie.Value {
		writeError(w, http.StatusUnauthorized, "nonce_mismatch", "nonce does not match")
		return
	}

	cfg := provider.Cfg
	ssoClaims := auth.SSOClaims{
		ExternalID:   idToken.Subject,
		ProviderSlug: slug,
		Username:     stringFromClaims(claimsMap, firstNonEmpty(cfg.UsernameClaim, "preferred_username")),
		Email:        stringFromClaims(claimsMap, firstNonEmpty(cfg.EmailClaim, "email")),
		FullName:     stringFromClaims(claimsMap, firstNonEmpty(cfg.NameClaim, "name")),
		Groups:       groupsFromClaims(claimsMap, firstNonEmpty(cfg.GroupsClaim, "groups")),
		// email_verified is only meaningful for the standard "email" claim; a
		// custom email claim has no verification assertion.
		EmailVerified: cfg.EmailClaim == "" || cfg.EmailClaim == "email",
	}
	ssoClaims.EmailVerified = ssoClaims.EmailVerified && boolClaimTrue(claimsMap, "email_verified")

	providerRow, err := h.db.GetSSOProviderBySlug(ctx, slug)
	if err != nil || providerRow == nil || !providerRow.Enabled {
		writeError(w, http.StatusNotFound, "provider_not_found", "SSO provider not found")
		return
	}

	user, err := auth.NewProvisioner(h.db, h.log).Provision(ctx, providerRow, ssoClaims)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotProvisioned) {
			writeError(w, http.StatusForbidden, "user_not_provisioned", "auto-provisioning is disabled for this provider")
			return
		}

		if errors.Is(err, auth.ErrAccountDisabled) {
			writeError(w, http.StatusForbidden, "account_disabled", "user account is disabled")
			return
		}

		if errors.Is(err, auth.ErrEmailAlreadyInUse) {
			writeError(w, http.StatusConflict, "email_already_in_use", "email is already associated with another account")
			return
		}

		if errors.Is(err, auth.ErrEmailLinkAmbiguous) {
			writeError(w, http.StatusInternalServerError, "email_link_ambiguous", "multiple accounts share this email")
			return
		}

		if errors.Is(err, auth.ErrIdentityConflict) {
			writeError(w, http.StatusConflict, "identity_conflict", "account is already linked to a different identity")
			return
		}

		h.log.Errorw("oidc callback: provision", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "user provisioning failed")

		return
	}

	csrfToken, err := middleware.NewCSRFToken()
	if err != nil {
		h.log.Errorw("oidc callback: generate CSRF token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")
		return
	}

	cookieValue, _, err := auth.CreateSession(ctx, h.db, user.ID, middleware.ClientIP(r, h.trustedProxies), r.UserAgent())
	if err != nil {
		h.log.Errorw("oidc callback: create session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")

		return
	}

	secure := isSecureContext(r, h.baseURL)
	setSessionCookie(w, secure, cookieValue)
	setCSRFCookie(w, secure, csrfToken)

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

func (h *OIDCHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	var body struct {
		IDTokenHint           string `json:"id_token_hint"`
		PostLogoutRedirectURI string `json:"post_logout_redirect_uri"`
	}

	_ = decodeJSON(r, &body)

	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie.Value != "" {
		_ = auth.DeleteSession(ctx, h.db, cookie.Value)
	}

	secure := isSecureContext(r, h.baseURL)
	clearSessionCookie(w, secure)
	clearCSRFCookie(w, secure)

	provider, err := auth.LoadOIDCProvider(ctx, h.db, slug, h.baseURL)
	if err != nil {
		writeNoContent(w)
		return
	}

	endSessionURL, ok := provider.EndSession(body.IDTokenHint, body.PostLogoutRedirectURI)
	if ok {
		writeJSON(w, http.StatusOK, map[string]string{"end_session_url": endSessionURL})
		return
	}

	writeNoContent(w)
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

func stringFromClaims(claims map[string]any, key string) string {
	if v, ok := claims[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}

	return ""
}

// boolClaimTrue reports whether claims[key] is boolean true. Some IdPs
// (e.g. Cognito) encode it as the string "true"; that is accepted too.
func boolClaimTrue(claims map[string]any, key string) bool {
	switch v := claims[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}

	return false
}

func groupsFromClaims(claims map[string]any, key string) []string {
	v, ok := claims[key]
	if !ok {
		return nil
	}

	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}

		return out
	}

	return nil
}
