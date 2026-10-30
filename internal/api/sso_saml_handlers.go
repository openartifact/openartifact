// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strings"

	"context"
	"time"

	"github.com/crewjam/saml"
	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// SAMLStore is the part of the database SAMLHandler needs.
type SAMLStore interface {
	establishSSOSessionStore
	ConsumeSAMLAssertion(ctx context.Context, providerSlug string, assertionID string, expiresAt time.Time) (bool, error)
	CreateSystemSettingIfAbsent(ctx context.Context, key string, value string) (bool, error)
	CreateUser(ctx context.Context, user *models.User) (*models.User, error)
	GetGroupByName(ctx context.Context, name string) (*models.Group, error)
	GetOrCreateGroup(ctx context.Context, name string) (*models.Group, error)
	GetOrCreatePermissionAssignment(ctx context.Context, targetID string, principalType string, principalID string) error
	GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error)
	GetSSOProviderBySlug(ctx context.Context, slug string) (*models.SSOProvider, error)
	GetSystemSetting(ctx context.Context, key string) (string, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	GetUserByExternalID(ctx context.Context, externalID string, providerSlug string) (*models.User, error)
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)
	SyncSSOGroups(ctx context.Context, userID string, groupIDs []string) error
	UpdateUserSSO(ctx context.Context, userID string, externalID string, providerSlug string) error
	UpdateUserSSOProfile(ctx context.Context, id string, email *string, fullName *string, lastLogin time.Time) error
}

type SAMLHandler struct {
	db        SAMLStore
	log       *logger.Logger
	evaluator *auth.Evaluator
	hmacKey   []byte
	baseURL   string
	cfg       *config.Config

	trustedProxies *middleware.TrustedProxies
}

func NewSAMLHandler(
	db SAMLStore,
	log *logger.Logger,
	evaluator *auth.Evaluator,
	hmacKey []byte,
	baseURL string,
	cfg *config.Config,
) *SAMLHandler {
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
		writeSSOLoadError(w, r, h.log, "saml login", slug, err, samlMetadataFailure())
		return
	}

	relayState := ""
	if redirect := r.URL.Query().Get("redirect"); redirect != "" && isOIDCRelativePath(redirect) {
		relayState = auth.SignRelayState(redirect, string(h.hmacKey))
	}

	loginURL, err := provider.StartLogin(w, relayState, h.hmacKey)
	if err != nil {
		h.log.Errorw("saml login: build login url", "slug", slug, "error", err)
		redirectSSOError(w, r, "internal_error")

		return
	}

	//nolint:gosec // G710: redirect URL is the configured IdP login URL
	http.Redirect(w, r, loginURL, http.StatusFound)
}

// samlACSErrors maps the auth package's SAML failures onto the sso_error code
// the ACS redirects to. Order matters: the first match wins. The client only
// sees the code; the detail (the wrapped error) is only logged.
//
//nolint:gochecknoglobals // immutable lookup table
var samlACSErrors = []struct {
	target error
	code   string
	logMsg string
}{
	{auth.ErrSAMLSignatureInvalid, "saml_signature_invalid", "saml acs: signature rejected"},
	{auth.ErrSAMLAudienceMismatch, "saml_audience_mismatch", "saml acs: audience rejected"},
	{auth.ErrSAMLAssertionExpired, "saml_assertion_expired", "saml acs: assertion expired"},
	{auth.ErrSAMLReplay, "saml_invalid", "saml acs: assertion replay rejected"},
	{auth.ErrSAMLRequestUntracked, "saml_invalid", "saml acs: response does not match a pending login request"},
	{auth.ErrSAMLRequestMismatch, "saml_invalid", "saml acs: InResponseTo rejected"},
	{auth.ErrSAMLAssertionNotYetValid, "saml_invalid", "saml acs: assertion not yet valid"},
	{auth.ErrSAMLDestinationMismatch, "saml_invalid", "saml acs: destination or recipient rejected"},
	{auth.ErrSAMLIssuerMismatch, "saml_invalid", "saml acs: issuer rejected"},
	{auth.ErrSAMLBadStatus, "saml_invalid", "saml acs: IdP reported a failure status"},
}

// writeACSParseError redirects a rejected response to the login page.
// Unrecognized failures are a generic saml_invalid, never the error text.
func (h *SAMLHandler) writeACSParseError(w http.ResponseWriter, r *http.Request, slug string, err error) {
	for _, m := range samlACSErrors {
		if errors.Is(err, m.target) {
			h.log.Warnw(m.logMsg, "slug", slug, "reason", err.Error())
			redirectSSOError(w, r, m.code)

			return
		}
	}

	h.log.Errorw("saml acs: parse callback", "slug", slug, "error", err)
	redirectSSOError(w, r, "saml_invalid")
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
		redirectSSOError(w, r, "invalid_relay_state")
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
		redirectSSOError(w, r, "saml_invalid")
		return
	}

	provider, err := auth.LoadSAMLProvider(ctx, h.db, slug, h.baseURL, h.cfg, h.log)
	if err != nil {
		writeSSOLoadError(w, r, h.log, "saml acs", slug, err, samlMetadataFailure())
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
		h.writeACSParseError(w, r, slug, err)
		return
	}

	externalID := ""
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		externalID = assertion.Subject.NameID.Value
	}

	if externalID == "" {
		redirectSSOError(w, r, "missing_subject")
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
		redirectSSOError(w, r, "provider_not_found")
		return
	}

	user, err := auth.NewProvisioner(h.db, h.log).Provision(ctx, providerRow, ssoClaims)
	if err != nil {
		writeSSOProvisionError(w, r, h.log, "saml acs", slug, err)
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
		if ssoProviderUnavailable(err) {
			writeError(w, http.StatusNotFound, "provider_not_found", "SSO provider not found")
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
