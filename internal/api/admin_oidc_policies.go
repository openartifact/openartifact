// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AdminOIDCPoliciesStore is the part of the database AdminOIDCPoliciesHandler needs.
type AdminOIDCPoliciesStore interface {
	CreateOIDCPolicy(ctx context.Context, p *models.OIDCTrustPolicy) (*models.OIDCTrustPolicy, error)
	GetOIDCPolicy(ctx context.Context, id string) (*models.OIDCTrustPolicy, error)
	ListOIDCPolicies(ctx context.Context) ([]*models.OIDCTrustPolicy, error)
	UpdateOIDCPolicy(ctx context.Context, p *models.OIDCTrustPolicy) (*models.OIDCTrustPolicy, error)
	DeleteOIDCPolicy(ctx context.Context, id string) (bool, error)
	GetServiceAccount(ctx context.Context, id string) (*models.User, error)
}

// AdminOIDCPoliciesHandler manages the CI OIDC trust policies that
// POST /api/v1/auth/oidc/exchange evaluates.
type AdminOIDCPoliciesHandler struct {
	db        AdminOIDCPoliciesStore
	log       *logger.Logger
	evaluator *auth.Evaluator
}

func NewAdminOIDCPoliciesHandler(db AdminOIDCPoliciesStore, log *logger.Logger, evaluator *auth.Evaluator) *AdminOIDCPoliciesHandler {
	return &AdminOIDCPoliciesHandler{db: db, log: log, evaluator: evaluator}
}

func (h *AdminOIDCPoliciesHandler) Register(r chi.Router) {
	r.Post("/api/v1/oidc-trust-policies", h.handleCreate)
	r.Get("/api/v1/oidc-trust-policies", h.handleList)
	r.With(requireUUIDParams("id")).Get("/api/v1/oidc-trust-policies/{id}", h.handleGet)
	r.With(requireUUIDParams("id")).Patch("/api/v1/oidc-trust-policies/{id}", h.handlePatch)
	r.With(requireUUIDParams("id")).Delete("/api/v1/oidc-trust-policies/{id}", h.handleDelete)
}

type oidcPolicyResponse struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Issuer           string                 `json:"issuer"`
	Audience         string                 `json:"audience"`
	Conditions       []models.OIDCCondition `json:"conditions"`
	ServiceAccountID string                 `json:"service_account_id"`
	ServiceAccount   string                 `json:"service_account"`
	TokenTTLSeconds  int                    `json:"token_ttl_seconds"`
	Enabled          bool                   `json:"enabled"`
	CreatedBy        string                 `json:"created_by"`
	CreatedAt        string                 `json:"created_at"`
	UpdatedAt        string                 `json:"updated_at"`
}

func oidcPolicyToResponse(p *models.OIDCTrustPolicy) oidcPolicyResponse {
	return oidcPolicyResponse{
		ID: p.ID, Name: p.Name, Issuer: p.Issuer, Audience: p.Audience, Conditions: p.Conditions,
		ServiceAccountID: p.ServiceAccountID, ServiceAccount: p.ServiceAccountName,
		TokenTTLSeconds: p.TokenTTLSeconds, Enabled: p.Enabled, CreatedBy: p.CreatedBy,
		CreatedAt: formatTime(p.CreatedAt), UpdatedAt: formatTime(p.UpdatedAt),
	}
}

// oidcPolicyBody is a create (every field but enabled and token_ttl_seconds
// required) or a patch (any field).
type oidcPolicyBody struct {
	Name             *string                 `json:"name"`
	Issuer           *string                 `json:"issuer"`
	Audience         *string                 `json:"audience"`
	Conditions       *[]models.OIDCCondition `json:"conditions"`
	ServiceAccountID *string                 `json:"service_account_id"`
	TokenTTLSeconds  *int                    `json:"token_ttl_seconds"`
	Enabled          *bool                   `json:"enabled"`
}

// apply copies the fields the body sets onto p.
func (b *oidcPolicyBody) apply(p *models.OIDCTrustPolicy) {
	if b.Name != nil {
		p.Name = *b.Name
	}

	if b.Issuer != nil {
		p.Issuer = *b.Issuer
	}

	if b.Audience != nil {
		p.Audience = *b.Audience
	}

	if b.Conditions != nil {
		p.Conditions = *b.Conditions
	}

	if b.ServiceAccountID != nil {
		p.ServiceAccountID = *b.ServiceAccountID
	}

	if b.TokenTTLSeconds != nil {
		p.TokenTTLSeconds = *b.TokenTTLSeconds
	}

	if b.Enabled != nil {
		p.Enabled = *b.Enabled
	}
}

// validatePolicy applies the content rules and the checks that need the
// database or the network: the issuer must pass the SSRF guard (checked when it
// is new or changed, so an old policy stays editable while its issuer is down)
// and the target must be a service account. A non-empty message is a validation
// failure for the administrator; an error is the database's.
func (h *AdminOIDCPoliciesHandler) validatePolicy(ctx context.Context, p *models.OIDCTrustPolicy, issuerChanged bool) (string, error) {
	if err := auth.ValidateOIDCPolicy(p); err != nil {
		return err.Error(), nil //nolint:nilerr // the message is the validation failure shown to the administrator
	}

	if issuerChanged {
		if err := requireHTTPSIdentityURL(p.Issuer); err != nil {
			return "issuer: " + err.Error(), nil //nolint:nilerr // see above
		}

		if err := netutil.ValidateOutboundURLContext(ctx, p.Issuer); err != nil {
			return "issuer: " + err.Error(), nil //nolint:nilerr // see above
		}
	}

	account, err := h.db.GetServiceAccount(ctx, p.ServiceAccountID)
	if err != nil {
		return "", err //nolint:wrapcheck // logged by the caller
	}

	if account == nil {
		return "service_account_id does not name a service account", nil
	}

	return "", nil
}

func (h *AdminOIDCPoliciesHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body oidcPolicyBody
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	if body.Name == nil || body.Issuer == nil || body.Audience == nil || body.Conditions == nil || body.ServiceAccountID == nil {
		writeError(w, http.StatusBadRequest, "validation_failed",
			"name, issuer, audience, conditions and service_account_id are required")

		return
	}

	policy := &models.OIDCTrustPolicy{Enabled: true}
	body.apply(policy)

	if !isUUIDString(policy.ServiceAccountID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "service_account_id must be a UUID")
		return
	}

	if !h.checkPolicy(w, r, policy, true) {
		return
	}

	if p := currentPrincipal(r); p != nil {
		policy.CreatedBy = p.Username
	}

	saved, err := h.db.CreateOIDCPolicy(r.Context(), policy)
	if err != nil {
		h.writeStoreError(w, err, "create")
		return
	}

	auditAdmin(r, audit.ActionOIDCPolicyCreated, "oidc_policy", saved.ID,
		"oidc policy "+saved.Name+" created for service account "+saved.ServiceAccountName)

	writeJSON(w, http.StatusCreated, oidcPolicyToResponse(saved))
}

// checkPolicy runs validatePolicy and writes the refusal.
func (h *AdminOIDCPoliciesHandler) checkPolicy(w http.ResponseWriter, r *http.Request, p *models.OIDCTrustPolicy, issuerChanged bool) bool {
	msg, err := h.validatePolicy(r.Context(), p, issuerChanged)
	if err != nil {
		h.log.Errorw("oidc policy validation lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return false
	}

	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return false
	}

	return true
}

func (h *AdminOIDCPoliciesHandler) writeStoreError(w http.ResponseWriter, err error, op string) {
	switch {
	case database.IsUniqueViolation(err):
		writeError(w, http.StatusConflict, "conflict", "a trust policy with this name already exists")
	case database.IsForeignKeyViolation(err):
		writeError(w, http.StatusBadRequest, "validation_failed", "service_account_id does not name a service account")
	default:
		h.log.Errorw("oidc policy "+op+" failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")
	}
}

func (h *AdminOIDCPoliciesHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	policies, err := h.db.ListOIDCPolicies(r.Context())
	if err != nil {
		h.log.Errorw("list oidc policies failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	data := make([]oidcPolicyResponse, 0, len(policies))
	for _, p := range policies {
		data = append(data, oidcPolicyToResponse(p))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (h *AdminOIDCPoliciesHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	policy, ok := h.load(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, oidcPolicyToResponse(policy))
}

// load fetches the policy of the request's {id}, writing 404/500 when it cannot.
func (h *AdminOIDCPoliciesHandler) load(w http.ResponseWriter, r *http.Request) (*models.OIDCTrustPolicy, bool) {
	policy, err := h.db.GetOIDCPolicy(r.Context(), urlparams.Vars(r)["id"])
	if err != nil {
		h.log.Errorw("get oidc policy failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if policy == nil {
		writeError(w, http.StatusNotFound, "not_found", "trust policy not found")
		return nil, false
	}

	return policy, true
}

func (h *AdminOIDCPoliciesHandler) handlePatch(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	var body oidcPolicyBody
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	policy, ok := h.load(w, r)
	if !ok {
		return
	}

	issuerChanged := body.Issuer != nil && *body.Issuer != policy.Issuer

	body.apply(policy)

	if !isUUIDString(policy.ServiceAccountID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "service_account_id must be a UUID")
		return
	}

	if !h.checkPolicy(w, r, policy, issuerChanged) {
		return
	}

	saved, err := h.db.UpdateOIDCPolicy(r.Context(), policy)
	if err != nil {
		h.writeStoreError(w, err, "update")
		return
	}

	if saved == nil {
		writeError(w, http.StatusNotFound, "not_found", "trust policy not found")
		return
	}

	auditAdmin(r, audit.ActionOIDCPolicyUpdated, "oidc_policy", saved.ID, "oidc policy "+saved.Name+" updated")

	writeJSON(w, http.StatusOK, oidcPolicyToResponse(saved))
}

func (h *AdminOIDCPoliciesHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	policy, ok := h.load(w, r)
	if !ok {
		return
	}

	deleted, err := h.db.DeleteOIDCPolicy(r.Context(), policy.ID)
	if err != nil {
		h.log.Errorw("delete oidc policy failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "trust policy not found")
		return
	}

	auditAdmin(r, audit.ActionOIDCPolicyDeleted, "oidc_policy", policy.ID, "oidc policy "+policy.Name+" deleted")

	writeNoContent(w)
}
