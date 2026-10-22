// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type SAMLHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	hmacKey   []byte
	baseURL   string
	cfg       *config.Config

	trustedProxies *middleware.TrustedProxies
}

func NewSAMLHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, hmacKey []byte, baseURL string, cfg *config.Config) *SAMLHandler {
	h := &SAMLHandler{db: db, log: log, evaluator: evaluator, hmacKey: hmacKey, baseURL: baseURL, cfg: cfg}
	if cfg != nil {
		h.trustedProxies = middleware.NewTrustedProxies(cfg.Server.TrustedProxies)
	}

	return h
}

// WithTrustedProxies overrides the reverse-proxy allowlist (default: the
// configured server.trustedProxies) used to resolve the client IP recorded on
// new sessions.
func (h *SAMLHandler) WithTrustedProxies(tp *middleware.TrustedProxies) *SAMLHandler {
	h.trustedProxies = tp
	return h
}

func (h *SAMLHandler) RegisterPublic(public chi.Router) {
	public.Get("/api/v1/auth/sso/saml/{slug}/login", h.handleLogin)
	public.Post("/api/v1/auth/sso/saml/{slug}/acs", h.handleACS)
	public.Get("/api/v1/auth/sso/saml/{slug}/metadata", h.handleMetadata)
}

func (h *SAMLHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	provider, err := auth.LoadSAMLProvider(ctx, h.db, slug, h.baseURL, h.cfg, h.log)
	if err != nil {
		if ssoProviderUnavailable(w, err) {
			return
		}

		if errors.Is(err, auth.ErrIDPMetadataUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "idp_metadata_unavailable", "IdP metadata unavailable")
			return
		}

		h.log.Errorw("saml login: load provider", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load provider")

		return
	}

	relayState := ""
	if redirect := r.URL.Query().Get("redirect"); redirect != "" && isOIDCRelativePath(redirect) {
		relayState = auth.SignRelayState(redirect, string(h.hmacKey))
	}

	loginURL, err := provider.StartLogin(w, relayState, h.hmacKey)
	if err != nil {
		h.log.Errorw("saml login: build login url", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to build login URL")

		return
	}

	//nolint:gosec // G710: redirect URL is the configured IdP login URL
	http.Redirect(w, r, loginURL, http.StatusFound)
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (h *SAMLHandler) handleACS(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "failed to parse form")
		return
	}

	// Validate RelayState before any side effect (assertion consumption,
	// provisioning, session creation).
	redirect := "/"

	if rs := r.Form.Get("RelayState"); rs != "" {
		decoded, err := auth.VerifyRelayState(rs, string(h.hmacKey))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_relay_state", "invalid relay state")
			return
		}

		if isOIDCRelativePath(decoded) {
			redirect = decoded
		}
	}

	provider, err := auth.LoadSAMLProvider(ctx, h.db, slug, h.baseURL, h.cfg, h.log)
	if err != nil {
		if ssoProviderUnavailable(w, err) {
			return
		}

		if errors.Is(err, auth.ErrIDPMetadataUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "idp_metadata_unavailable", "IdP metadata unavailable")
			return
		}

		h.log.Errorw("saml acs: load provider", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load provider")

		return
	}

	assertion, err := provider.ParseCallback(w, r, h.db, h.hmacKey)
	if err != nil {
		if errors.Is(err, auth.ErrSAMLSignatureInvalid) {
			writeError(w, http.StatusBadRequest, "saml_signature_invalid", "SAML signature invalid")
			return
		}

		if errors.Is(err, auth.ErrSAMLAudienceMismatch) {
			writeError(w, http.StatusBadRequest, "saml_audience_mismatch", "SAML audience mismatch")
			return
		}

		if errors.Is(err, auth.ErrSAMLAssertionExpired) {
			writeError(w, http.StatusUnauthorized, "saml_assertion_expired", "SAML assertion expired")
			return
		}

		if errors.Is(err, auth.ErrSAMLReplay) {
			h.log.Warnw("saml acs: assertion replay rejected", "slug", slug)
			writeError(w, http.StatusUnauthorized, "saml_invalid", "SAML response invalid")

			return
		}

		if errors.Is(err, auth.ErrSAMLRequestUntracked) {
			h.log.Warnw("saml acs: response does not match a pending login request", "slug", slug)
			writeError(w, http.StatusBadRequest, "saml_invalid", "SAML response invalid")

			return
		}

		h.log.Errorw("saml acs: parse callback", "slug", slug, "error", err)
		writeError(w, http.StatusBadRequest, "saml_invalid", "SAML response invalid")

		return
	}

	externalID := ""
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		externalID = assertion.Subject.NameID.Value
	}

	if externalID == "" {
		writeError(w, http.StatusBadRequest, "missing_subject", "SAML assertion missing subject NameID")
		return
	}

	cfg := provider.Cfg
	usernameAttr := firstNonEmpty(cfg.UsernameAttribute, "uid")
	emailAttr := firstNonEmpty(cfg.EmailAttribute, "mail")
	nameAttr := firstNonEmpty(cfg.NameAttribute, "displayName")
	groupsAttr := cfg.GroupsAttribute

	username := ""
	email := ""
	fullName := ""
	emailVerifiedAttr := ""

	var groups []string

	for _, stmt := range assertion.AttributeStatements {
		for _, attr := range stmt.Attributes {
			switch attr.Name {
			case usernameAttr:
				if len(attr.Values) > 0 {
					username = attr.Values[0].Value
				}
			case emailAttr:
				if len(attr.Values) > 0 {
					email = attr.Values[0].Value
				}
			case nameAttr:
				if len(attr.Values) > 0 {
					fullName = attr.Values[0].Value
				}
			case groupsAttr:
				if groupsAttr != "" {
					for _, v := range attr.Values {
						groups = append(groups, v.Value)
					}
				}
			}

			if attr.Name == "email_verified" && len(attr.Values) > 0 {
				emailVerifiedAttr = attr.Values[0].Value
			}
		}
	}

	ssoClaims := auth.SSOClaims{
		ExternalID:   externalID,
		ProviderSlug: slug,
		Username:     username,
		Email:        email,
		// The assertion is signed by the configured IdP, which is the authority
		// for the attributes, but SAML has no standard verification flag, so the
		// email counts as verified only when the IdP explicitly sends
		// email_verified=true (a missing attribute must not enable link_by_email
		// takeover of a local account).
		EmailVerified: samlEmailVerified(emailVerifiedAttr),
		FullName:      fullName,
		Groups:        groups,
	}

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

		h.log.Errorw("saml acs: provision", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "user provisioning failed")

		return
	}

	csrfToken, err := middleware.NewCSRFToken()
	if err != nil {
		h.log.Errorw("saml acs: generate CSRF token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")
		return
	}

	cookieValue, _, err := auth.CreateSession(ctx, h.db, user.ID, middleware.ClientIP(r, h.trustedProxies), r.UserAgent())
	if err != nil {
		h.log.Errorw("saml acs: create session", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "session creation failed")

		return
	}

	secure := isSecureContext(r, h.baseURL)
	setSessionCookie(w, secure, cookieValue)
	setCSRFCookie(w, secure, csrfToken)

	//nolint:gosec // G710: redirect is a same-origin relative path (safeRedirectPath)
	http.Redirect(w, r, safeRedirectPath(redirect), http.StatusFound)
}

func (h *SAMLHandler) handleMetadata(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	provider, err := auth.LoadSAMLProvider(ctx, h.db, slug, h.baseURL, h.cfg, h.log)
	if err != nil {
		if ssoProviderUnavailable(w, err) {
			return
		}

		h.log.Errorw("saml metadata: load provider", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load provider")

		return
	}

	xmlBytes, err := provider.MetadataXML()
	if err != nil {
		h.log.Errorw("saml metadata: serialize", "slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate metadata")

		return
	}

	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: XML response to SAML SP, not HTML output
	_, _ = w.Write(xmlBytes)
}

// samlEmailVerified reports whether the IdP explicitly asserted a verified
// email. SAML has no standard flag, so a missing or any other value is treated
// as unverified: defaulting to verified would let an IdP user who controls
// their own mail attribute take over a local account through link_by_email.
func samlEmailVerified(attr string) bool {
	return strings.EqualFold(strings.TrimSpace(attr), "true")
}
