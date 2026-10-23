// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/crewjam/saml"
	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
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

// samlACSErrors maps the auth package's SAML failures onto the ACS response.
// Order matters: the first match wins. Every entry answers with a generic
// message; the detail (the wrapped error) is only logged.
//
//nolint:gochecknoglobals // immutable lookup table
var samlACSErrors = []struct {
	target error
	status int
	code   string
	msg    string
	// quiet failures are expected noise (replays, stray posts): logged at warn.
	logMsg string
}{
	{auth.ErrSAMLSignatureInvalid, http.StatusBadRequest, "saml_signature_invalid", "SAML signature invalid", "saml acs: signature rejected"},
	{auth.ErrSAMLAudienceMismatch, http.StatusBadRequest, "saml_audience_mismatch", "SAML audience mismatch", "saml acs: audience rejected"},
	{auth.ErrSAMLAssertionExpired, http.StatusUnauthorized, "saml_assertion_expired", "SAML assertion expired", "saml acs: assertion expired"},
	{auth.ErrSAMLReplay, http.StatusUnauthorized, "saml_invalid", "SAML response invalid", "saml acs: assertion replay rejected"},
	{auth.ErrSAMLRequestUntracked, http.StatusBadRequest, "saml_invalid", "SAML response invalid", "saml acs: response does not match a pending login request"},
	{auth.ErrSAMLRequestMismatch, http.StatusBadRequest, "saml_invalid", "SAML response invalid", "saml acs: InResponseTo rejected"},
	{auth.ErrSAMLAssertionNotYetValid, http.StatusBadRequest, "saml_invalid", "SAML response invalid", "saml acs: assertion not yet valid"},
	{auth.ErrSAMLDestinationMismatch, http.StatusBadRequest, "saml_invalid", "SAML response invalid", "saml acs: destination or recipient rejected"},
	{auth.ErrSAMLIssuerMismatch, http.StatusBadRequest, "saml_invalid", "SAML response invalid", "saml acs: issuer rejected"},
	{auth.ErrSAMLBadStatus, http.StatusBadRequest, "saml_invalid", "SAML response invalid", "saml acs: IdP reported a failure status"},
}

// writeACSParseError answers a rejected response. Unrecognized failures are a
// generic 400 saml_invalid, never the error text.
func (h *SAMLHandler) writeACSParseError(w http.ResponseWriter, slug string, err error) {
	for _, m := range samlACSErrors {
		if errors.Is(err, m.target) {
			h.log.Warnw(m.logMsg, "slug", slug, "reason", err.Error())
			writeError(w, m.status, m.code, m.msg)

			return
		}
	}

	h.log.Errorw("saml acs: parse callback", "slug", slug, "error", err)
	writeError(w, http.StatusBadRequest, "saml_invalid", "SAML response invalid")
}

// acsRedirect decides where a successful login lands. SP-initiated RelayState
// is HMAC-signed by this server and verified before any side effect. An
// IdP-initiated RelayState is chosen and unsigned by the IdP (or whoever posts
// the response), so it is honored only as a same-origin relative path and
// otherwise ignored: never rejected, never followed off-site.
func (h *SAMLHandler) acsRedirect(w http.ResponseWriter, r *http.Request, mode auth.SAMLResponseMode) (string, bool) {
	rs := r.Form.Get("RelayState")
	if rs == "" {
		return "/", true
	}

	if mode == auth.SAMLIDPInitiated {
		if isOIDCRelativePath(rs) {
			return rs, true
		}

		return "/", true
	}

	decoded, err := auth.VerifyRelayState(rs, string(h.hmacKey))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_relay_state", "invalid relay state")
		return "", false
	}

	if isOIDCRelativePath(decoded) {
		return decoded, true
	}

	return "/", true
}

func (h *SAMLHandler) handleACS(w http.ResponseWriter, r *http.Request) {
	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "failed to parse form")
		return
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

	// Validate RelayState before any side effect (assertion consumption,
	// provisioning, session creation).
	mode := provider.ResponseMode(r)

	redirect, ok := h.acsRedirect(w, r, mode)
	if !ok {
		return
	}

	assertion, err := provider.ParseCallback(w, r, h.db, h.hmacKey)
	if err != nil {
		h.writeACSParseError(w, slug, err)
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

	attrs := readSAMLAttributes(assertion, provider.Cfg)

	ssoClaims := auth.SSOClaims{
		ExternalID:   externalID,
		ProviderSlug: slug,
		Username:     attrs.username,
		Email:        attrs.email,
		// The assertion is signed by the configured IdP, which is the authority
		// for the attributes, but SAML has no standard verification flag, so the
		// email counts as verified only when the IdP explicitly sends
		// email_verified=true (a missing attribute must not enable link_by_email
		// takeover of a local account).
		EmailVerified: samlEmailVerified(attrs.emailVerified),
		FullName:      attrs.fullName,
		Groups:        attrs.groups,
		// Only a configured groups_attribute that the assertion carries counts as
		// the IdP stating the user's groups; otherwise existing memberships stay.
		GroupsPresent: attrs.groupsPresent,
	}

	providerRow, err := h.db.GetSSOProviderBySlug(ctx, slug)
	if err != nil || providerRow == nil || !providerRow.Enabled {
		writeError(w, http.StatusNotFound, "provider_not_found", "SSO provider not found")
		return
	}

	user, err := auth.NewProvisioner(h.db, h.log).Provision(ctx, providerRow, ssoClaims)
	if err != nil {
		writeSSOProvisionError(w, h.log, "saml acs", slug, err)
		return
	}

	if !establishSSOSession(w, r, h.db, h.log, h.baseURL, h.trustedProxies, ssoSessionParams{
		UserID: user.ID, ProviderSlug: slug,
	}) {
		return
	}

	if mode == auth.SAMLIDPInitiated {
		h.log.Infow("saml acs: idp-initiated login", "slug", slug)
	}

	//nolint:gosec // G710: redirect is a same-origin relative path (safeRedirectPath)
	http.Redirect(w, r, safeRedirectPath(redirect), http.StatusFound)
}

type samlAttributes struct {
	username, email, fullName, emailVerified string
	groups                                   []string
	groupsPresent                            bool
}

// readSAMLAttributes extracts the mapped attributes from a verified assertion.
// Attributes match by Name. The groups are "present" only when a
// groups_attribute is configured and the assertion carries it, even with no
// values (an explicit empty group set); an absent attribute says nothing.
func readSAMLAttributes(assertion *saml.Assertion, cfg models.SAMLConfig) samlAttributes {
	out := samlAttributes{}

	// One attribute may feed several fields (username_attribute and
	// email_attribute are often both "mail").
	targets := map[string][]*string{}
	bind := func(name string, dst *string) { targets[name] = append(targets[name], dst) }

	bind(firstNonEmpty(cfg.UsernameAttribute, "uid"), &out.username)
	bind(firstNonEmpty(cfg.EmailAttribute, "mail"), &out.email)
	bind(firstNonEmpty(cfg.NameAttribute, "displayName"), &out.fullName)
	bind("email_verified", &out.emailVerified)

	for _, stmt := range assertion.AttributeStatements {
		for _, attr := range stmt.Attributes {
			if cfg.GroupsAttribute != "" && attr.Name == cfg.GroupsAttribute {
				out.groupsPresent = true

				for _, v := range attr.Values {
					if v.Value != "" {
						out.groups = append(out.groups, v.Value)
					}
				}

				continue
			}

			if len(attr.Values) == 0 {
				continue
			}

			for _, dst := range targets[attr.Name] {
				*dst = attr.Values[0].Value
			}
		}
	}

	return out
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
