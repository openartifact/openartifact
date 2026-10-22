// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/crewjam/saml/samlsp"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

var slugRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

type AdminSSOHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	baseURL   string
	cfg       *config.Config
}

func NewAdminSSOHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, baseURL string, cfg *config.Config) *AdminSSOHandler {
	return &AdminSSOHandler{db: db, log: log, evaluator: evaluator, baseURL: baseURL, cfg: cfg}
}

func (h *AdminSSOHandler) RegisterAuthed(authed chi.Router) {
	authed.Post("/api/v1/admin/sso-providers", h.handleCreate)
	authed.Get("/api/v1/admin/sso-providers", h.handleList)
	authed.Get("/api/v1/admin/sso-providers/{slug}", h.handleGet)
	authed.Patch("/api/v1/admin/sso-providers/{slug}", h.handleUpdate)
	authed.Delete("/api/v1/admin/sso-providers/{slug}", h.handleDelete)
	authed.Post("/api/v1/admin/sso-providers/{slug}/test", h.handleTest)
}

func (h *AdminSSOHandler) RegisterPublic(r chi.Router) {
	r.Get("/api/v1/auth/sso/providers", h.handlePublicList)
}

func (h *AdminSSOHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(r.Context(), p, h.evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return false
	}

	if !isAdmin {
		writeError(w, http.StatusForbidden, "admin_required", "admin access required")
		return false
	}

	return true
}

const (
	maxSSOMappingEntries = 256
	maxSSOFieldLen       = 512
)

var errIdentityURLNotHTTPS = errors.New("URL must use https (http is only allowed for a loopback host)")

// requireHTTPSIdentityURL rejects plain-http IdP trust-root URLs: discovery
// documents, JWKS and SAML metadata carry the signing keys, so a network
// attacker able to alter them could forge identities. http is tolerated only
// for loopback hosts (local development IdPs).
func requireHTTPSIdentityURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip, perr := netip.ParseAddr(host); perr == nil && ip.IsLoopback() || host == "localhost" {
			return nil
		}
	}

	return errIdentityURLNotHTTPS
}

// validateOIDCConfig applies the create-time rules to a (plaintext-secret)
// OIDC config: required fields, outbound-URL safety, and sane claim names.
func validateOIDCConfig(cfg *models.OIDCConfig) error {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return errors.New("oidc_config requires issuer, client_id, and client_secret") //nolint:err113 // validation message surfaced to the admin
	}

	if err := requireHTTPSIdentityURL(cfg.Issuer); err != nil {
		return fmt.Errorf("oidc_config.issuer: %w", err)
	}

	if err := netutil.ValidateOutboundURL(cfg.Issuer); err != nil {
		return fmt.Errorf("oidc_config.issuer: %w", err)
	}

	if crypto.IsEncrypted(cfg.ClientSecret) {
		return errors.New("oidc_config.client_secret must be the plaintext secret") //nolint:err113 // validation message surfaced to the admin
	}

	for name, v := range map[string]string{
		"username_claim": cfg.UsernameClaim, "email_claim": cfg.EmailClaim,
		"name_claim": cfg.NameClaim, "groups_claim": cfg.GroupsClaim,
	} {
		if err := validateMappingName("oidc_config."+name, v); err != nil {
			return err
		}
	}

	for _, s := range cfg.Scopes {
		if s == "" || strings.ContainsAny(s, " \t\r\n") {
			return errors.New("oidc_config.scopes entries must be non-empty and contain no whitespace") //nolint:err113 // validation message surfaced to the admin
		}
	}

	return nil
}

// validateSAMLConfig applies the create-time rules to a SAML config.
func validateSAMLConfig(cfg *models.SAMLConfig) error {
	if cfg.IDPMetadataURL == "" && cfg.IDPMetadataXML == "" {
		return errors.New("saml_config requires idp_metadata_url or idp_metadata_xml") //nolint:err113 // validation message surfaced to the admin
	}

	if cfg.IDPMetadataURL != "" {
		if err := requireHTTPSIdentityURL(cfg.IDPMetadataURL); err != nil {
			return fmt.Errorf("saml_config.idp_metadata_url: %w", err)
		}

		if err := netutil.ValidateOutboundURL(cfg.IDPMetadataURL); err != nil {
			return fmt.Errorf("saml_config.idp_metadata_url: %w", err)
		}
	}

	if cfg.IDPMetadataXML != "" {
		if _, err := samlsp.ParseMetadata([]byte(cfg.IDPMetadataXML)); err != nil {
			return fmt.Errorf("saml_config.idp_metadata_xml is not valid IdP metadata: %w", err)
		}
	}

	for name, v := range map[string]string{
		"username_attribute": cfg.UsernameAttribute, "email_attribute": cfg.EmailAttribute,
		"name_attribute": cfg.NameAttribute, "groups_attribute": cfg.GroupsAttribute,
	} {
		if err := validateMappingName("saml_config."+name, v); err != nil {
			return err
		}
	}

	return nil
}

func validateMappingName(field, v string) error {
	if len(v) > maxSSOFieldLen || strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("%s is invalid", field) //nolint:err113 // validation message surfaced to the admin
	}

	return nil
}

// validateStringMap checks a group/permission mapping: bounded size, no empty
// keys or values.
func validateStringMap(field string, m map[string]string) error {
	if len(m) > maxSSOMappingEntries {
		return fmt.Errorf("%s has too many entries", field) //nolint:err113 // validation message surfaced to the admin
	}

	for k, v := range m {
		if k == "" || v == "" || len(k) > maxSSOFieldLen || len(v) > maxSSOFieldLen {
			return fmt.Errorf("%s entries must have non-empty keys and values", field) //nolint:err113 // validation message surfaced to the admin
		}
	}

	return nil
}

// validateJSONObjectOrNull accepts only a JSON object or null (claim_mapping).
func validateJSONObjectOrNull(field string, raw json.RawMessage) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}

	var m map[string]any
	if err := json.Unmarshal(t, &m); err != nil {
		return fmt.Errorf("%s must be a JSON object", field) //nolint:err113 // validation message surfaced to the admin
	}

	return nil
}

// sealOIDCConfig returns cfg marshalled with the client secret encrypted.
func sealOIDCConfig(cfg models.OIDCConfig) (json.RawMessage, error) {
	enc, err := crypto.Encrypt(cfg.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt client_secret: %w", err)
	}

	cfg.ClientSecret = enc

	b, err := json.Marshal(cfg) //nolint:gosec // G117: the secret is encrypted (or key-less passthrough) for storage
	if err != nil {
		return nil, fmt.Errorf("marshal oidc_config: %w", err)
	}

	return b, nil
}

func (h *AdminSSOHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	var body models.SSOProvider
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !slugRe.MatchString(body.Slug) {
		writeError(w, http.StatusBadRequest, "validation_failed", "slug must match ^[a-z0-9_-]{1,64}$")
		return
	}

	if body.Kind != "oidc" && body.Kind != "saml" {
		writeError(w, http.StatusBadRequest, "validation_failed", "kind must be 'oidc' or 'saml'")
		return
	}

	if body.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "display_name is required")
		return
	}

	if err := validateStringMap("group_mapping", body.GroupMapping); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if err := validateStringMap("permission_mapping", body.PermissionMapping); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	if err := validateJSONObjectOrNull("claim_mapping", body.ClaimMapping); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	switch body.Kind {
	case "oidc":
		var cfg models.OIDCConfig
		if err := json.Unmarshal(body.OIDCConfig, &cfg); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "oidc_config requires issuer, client_id, and client_secret")
			return
		}

		if err := validateOIDCConfig(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		sealed, err := sealOIDCConfig(cfg)
		if err != nil {
			h.log.Errorw("admin sso create: seal oidc config", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to protect client secret")

			return
		}

		body.OIDCConfig = sealed
		body.SAMLConfig = nil
	case "saml":
		var cfg models.SAMLConfig
		if err := json.Unmarshal(body.SAMLConfig, &cfg); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", "saml_config requires idp_metadata_url or idp_metadata_xml")
			return
		}

		if err := validateSAMLConfig(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}

		body.OIDCConfig = nil
	}

	now := time.Now()
	body.ID = uuid.New().String()
	body.CreatedAt = now
	body.UpdatedAt = now
	body.Enabled = true

	if err := h.db.CreateSSOProvider(r.Context(), &body); err != nil {
		h.log.Errorw("admin sso create: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionSSOProviderCreated, "sso_provider", body.ID, "sso provider "+body.Slug+" created")

	writeJSON(w, http.StatusCreated, ssoProviderToResponse(&body))
}

func (h *AdminSSOHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	providers, err := h.db.ListAllSSOProviders(r.Context())
	if err != nil {
		h.log.Errorw("admin sso list: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	resp := make([]any, 0, len(providers))
	for _, p := range providers {
		resp = append(resp, ssoProviderToResponse(p))
	}

	writeJSON(w, http.StatusOK, map[string]any{"providers": resp})
}

func (h *AdminSSOHandler) handlePublicList(w http.ResponseWriter, r *http.Request) {
	providers, err := h.db.ListEnabledSSOProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
		return
	}

	resp := make([]map[string]any, 0, len(providers))
	for _, p := range providers {
		resp = append(resp, map[string]any{
			"slug":         p.Slug,
			"kind":         p.Kind,
			"display_name": p.DisplayName,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"providers": resp})
}

func (h *AdminSSOHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	slug := urlparams.Vars(r)["slug"]

	provider, err := h.db.GetSSOProviderBySlug(r.Context(), slug)
	if err != nil {
		h.log.Errorw("admin sso get: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if provider == nil {
		writeError(w, http.StatusNotFound, "not_found", "SSO provider not found")
		return
	}

	writeJSON(w, http.StatusOK, ssoProviderToResponse(provider))
}

// patchString decodes a JSON string patch field; ok is false on a type error.
func patchString(raw json.RawMessage) (string, bool) {
	var s string
	return s, json.Unmarshal(raw, &s) == nil
}

func patchBool(raw json.RawMessage) (bool, bool) {
	var b bool
	return b, json.Unmarshal(raw, &b) == nil
}

// isJSONObject reports whether raw is a JSON object (not null/array/scalar).
func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// mergeOIDCPatch overlays the patch onto the stored config: fields absent from
// the patch keep their stored values, and an absent or empty client_secret
// keeps the stored (encrypted) secret. A newly supplied secret is encrypted.
func mergeOIDCPatch(stored json.RawMessage, patch json.RawMessage) (json.RawMessage, error) {
	if !isJSONObject(patch) {
		return nil, errors.New("oidc_config must be a JSON object") //nolint:err113 // validation message surfaced to the admin
	}

	var merged models.OIDCConfig
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &merged); err != nil {
			return nil, fmt.Errorf("stored oidc_config is unreadable: %w", err)
		}
	}

	storedSecret := merged.ClientSecret

	// Unmarshalling into the populated struct only touches fields present in the patch.
	if err := json.Unmarshal(patch, &merged); err != nil {
		return nil, errors.New("oidc_config has invalid field types") //nolint:err113 // validation message surfaced to the admin
	}

	if merged.ClientSecret == "" {
		merged.ClientSecret = storedSecret
	}

	// Untouched encrypted secret: keep the ciphertext verbatim (it cannot be
	// validated as plaintext, so validate everything else with a placeholder).
	if merged.ClientSecret == storedSecret && crypto.IsEncrypted(storedSecret) {
		probe := merged
		probe.ClientSecret = "placeholder"

		if err := validateOIDCConfig(&probe); err != nil {
			return nil, err
		}

		b, err := json.Marshal(merged) //nolint:gosec // G117: stored secret is carried through unchanged (encrypted at rest)
		if err != nil {
			return nil, fmt.Errorf("marshal oidc_config: %w", err)
		}

		return b, nil
	}

	// New secret, or a legacy plaintext one that is sealed on this write.
	if err := validateOIDCConfig(&merged); err != nil {
		return nil, err
	}

	return sealOIDCConfig(merged)
}

func mergeSAMLPatch(stored json.RawMessage, patch json.RawMessage) (json.RawMessage, error) {
	if !isJSONObject(patch) {
		return nil, errors.New("saml_config must be a JSON object") //nolint:err113 // validation message surfaced to the admin
	}

	var merged models.SAMLConfig
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &merged); err != nil {
			return nil, fmt.Errorf("stored saml_config is unreadable: %w", err)
		}
	}

	if err := json.Unmarshal(patch, &merged); err != nil {
		return nil, errors.New("saml_config has invalid field types") //nolint:err113 // validation message surfaced to the admin
	}

	if err := validateSAMLConfig(&merged); err != nil {
		return nil, err
	}

	b, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal saml_config: %w", err)
	}

	return b, nil
}

func (h *AdminSSOHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	existing, err := h.db.GetSSOProviderBySlug(ctx, slug)
	if err != nil {
		h.log.Errorw("admin sso update: db get", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "SSO provider not found")
		return
	}

	var patch map[string]json.RawMessage
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if status, code, msg := applySSOPatch(existing, patch); status != 0 {
		writeError(w, status, code, msg)
		return
	}

	existing.UpdatedAt = time.Now()

	if err := h.db.UpdateSSOProvider(ctx, existing); err != nil {
		h.log.Errorw("admin sso update: db update", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionSSOProviderUpdated, "sso_provider", existing.ID, "sso provider "+existing.Slug+" updated")

	writeJSON(w, http.StatusOK, ssoProviderToResponse(existing))
}

// applySSOPatch merges patch into existing, validating exactly what create
// validates. On failure it returns the HTTP status, error code and message
// (status 0 means success).
func applySSOPatch(existing *models.SSOProvider, patch map[string]json.RawMessage) (int, string, string) {
	invalid := func(msg string) (int, string, string) {
		return http.StatusBadRequest, "validation_failed", msg
	}

	if v, ok := patch["display_name"]; ok {
		s, ok := patchString(v)
		if !ok || s == "" {
			return invalid("display_name must be a non-empty string")
		}

		existing.DisplayName = s
	}

	for field, dst := range map[string]*bool{
		"enabled":              &existing.Enabled,
		"auto_provision_users": &existing.AutoProvisionUsers,
		"link_by_email":        &existing.LinkByEmail,
	} {
		if v, ok := patch[field]; ok {
			b, ok := patchBool(v)
			if !ok {
				return invalid(field + " must be a boolean")
			}

			*dst = b
		}
	}

	if v, ok := patch["oidc_config"]; ok {
		if existing.Kind != "oidc" {
			return invalid("oidc_config is only valid for oidc providers")
		}

		merged, err := mergeOIDCPatch(existing.OIDCConfig, v)
		if err != nil {
			return invalid(err.Error())
		}

		existing.OIDCConfig = merged
	}

	if v, ok := patch["saml_config"]; ok {
		if existing.Kind != "saml" {
			return invalid("saml_config is only valid for saml providers")
		}

		merged, err := mergeSAMLPatch(existing.SAMLConfig, v)
		if err != nil {
			return invalid(err.Error())
		}

		existing.SAMLConfig = merged
	}

	if v, ok := patch["claim_mapping"]; ok {
		if err := validateJSONObjectOrNull("claim_mapping", v); err != nil {
			return invalid(err.Error())
		}

		existing.ClaimMapping = v
	}

	for field, dst := range map[string]*map[string]string{
		"group_mapping":      &existing.GroupMapping,
		"permission_mapping": &existing.PermissionMapping,
	} {
		if v, ok := patch[field]; ok {
			var m map[string]string
			if err := json.Unmarshal(v, &m); err != nil {
				return invalid(field + " must be an object of strings")
			}

			if err := validateStringMap(field, m); err != nil {
				return invalid(err.Error())
			}

			*dst = m
		}
	}

	return 0, "", ""
}

func (h *AdminSSOHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	existing, err := h.db.GetSSOProviderBySlug(ctx, slug)
	if err != nil {
		h.log.Errorw("admin sso delete: db get", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "SSO provider not found")
		return
	}

	h.log.Warnw("admin sso: hard deleting SSO provider", "slug", slug, "id", existing.ID)

	if err := h.db.DeleteSSOProvider(ctx, existing.ID); err != nil {
		h.log.Errorw("admin sso delete: db delete", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	auditAdmin(r, audit.ActionSSOProviderDeleted, "sso_provider", existing.ID, "sso provider "+existing.Slug+" deleted")

	writeNoContent(w)
}

func (h *AdminSSOHandler) handleTest(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	slug := urlparams.Vars(r)["slug"]
	ctx := r.Context()

	existing, err := h.db.GetSSOProviderBySlug(ctx, slug)
	if err != nil {
		h.log.Errorw("admin sso test: db get", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if existing == nil {
		writeError(w, http.StatusNotFound, "not_found", "SSO provider not found")
		return
	}

	switch existing.Kind {
	case "oidc":
		_, err := auth.LoadOIDCProviderRow(ctx, existing, h.baseURL)
		if err != nil {
			h.log.Warnw("admin sso test: oidc load failed", "slug", existing.Slug, "error", err)

			if errors.Is(err, auth.ErrOIDCDiscoveryFailed) {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":      false,
					"error":   "oidc_discovery_failed",
					"message": ssoTestMessage(true),
				})

				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"ok":      false,
				"error":   "load_failed",
				"message": ssoTestMessage(false),
			})

			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": "oidc"})

	case "saml":
		_, err := auth.LoadSAMLProviderRow(ctx, existing, h.baseURL, h.cfg, h.log)
		if err != nil {
			h.log.Warnw("admin sso test: saml load failed", "slug", existing.Slug, "error", err)

			if errors.Is(err, auth.ErrIDPMetadataUnavailable) {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":      false,
					"error":   "idp_metadata_unavailable",
					"message": ssoTestMessage(true),
				})

				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"ok":      false,
				"error":   "load_failed",
				"message": ssoTestMessage(false),
			})

			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": "saml"})

	default:
		writeError(w, http.StatusBadRequest, "unknown_kind", "unknown provider kind")
	}
}

func ssoProviderToResponse(p *models.SSOProvider) responses.SSOProviderResponse {
	var oidcCfg map[string]any

	if len(p.OIDCConfig) > 0 {
		var cfg models.OIDCConfig
		if err := json.Unmarshal(p.OIDCConfig, &cfg); err == nil {
			hasSecret := cfg.ClientSecret != ""
			cfg.ClientSecret = ""

			if b, err := json.Marshal(cfg); err == nil { //nolint:gosec // G117: ClientSecret is cleared above; the value is never serialized
				_ = json.Unmarshal(b, &oidcCfg)
			}

			// Secrets are write-only: report presence, never the value.
			delete(oidcCfg, "client_secret")

			if oidcCfg != nil {
				oidcCfg["has_secret"] = hasSecret
			}
		}
	}

	var samlCfg map[string]any
	if len(p.SAMLConfig) > 0 {
		_ = json.Unmarshal(p.SAMLConfig, &samlCfg)
	}

	var claimMapping map[string]any
	if len(p.ClaimMapping) > 0 {
		_ = json.Unmarshal(p.ClaimMapping, &claimMapping)
	}

	return responses.SSOProviderResponse{
		ID:                 p.ID,
		Slug:               p.Slug,
		Kind:               p.Kind,
		DisplayName:        p.DisplayName,
		Enabled:            p.Enabled,
		OIDCConfig:         oidcCfg,
		SAMLConfig:         samlCfg,
		ClaimMapping:       claimMapping,
		GroupMapping:       p.GroupMapping,
		PermissionMapping:  p.PermissionMapping,
		AutoProvisionUsers: p.AutoProvisionUsers,
		LinkByEmail:        p.LinkByEmail,
		CreatedAt:          formatTime(p.CreatedAt),
		UpdatedAt:          formatTime(p.UpdatedAt),
	}
}

// ssoTestMessage is the fixed /test failure text. The underlying error is
// never echoed: it would turn the endpoint into an SSRF response oracle
// (connection refused vs. TLS vs. blocked address vs. HTTP status).
func ssoTestMessage(unreachable bool) string {
	if unreachable {
		return "the identity provider could not be reached or returned an invalid response; see server logs"
	}

	return "the provider configuration could not be loaded; see server logs"
}
