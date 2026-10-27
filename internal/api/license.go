// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/license"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type LicenseHandler struct {
	repoAccess

	detector *license.Detector
	licenses artifactLicenseSource
}

// NewLicenseHandler returns the license scan and policy handler. blobs is the
// store the scan reads artifact files from to find their declared licenses.
func NewLicenseHandler(
	db database.Database,
	log *logger.Logger,
	evaluator *auth.Evaluator,
	detector *license.Detector,
	blobs storage.BlobStore,
) *LicenseHandler {
	return &LicenseHandler{
		repoAccess: newRepoAccess(db, log, evaluator),
		detector:   detector,
		licenses:   artifactLicenseSource{db: db, blobs: blobs, log: log, detector: detector},
	}
}

func (h *LicenseHandler) RegisterRoutes(authedRouter chi.Router) {
	// License scan results
	authedRouter.Post("/api/v1/repositories/{name}/license/scan", h.handleScan)
	authedRouter.Get("/api/v1/repositories/{name}/license/scans", h.handleListScans)
	authedRouter.Get("/api/v1/repositories/{name}/license/scans/{id}", h.handleGetScan)
	authedRouter.Post("/api/v1/repositories/{name}/license/scans/{id}/quarantine", h.handleQuarantine)

	// License policies
	authedRouter.Get("/api/v1/repositories/{name}/license/policies", h.handleListPolicies)
	authedRouter.Post("/api/v1/repositories/{name}/license/policies", h.handleCreatePolicy)
	authedRouter.Put("/api/v1/repositories/{name}/license/policies/{id}", h.handleUpdatePolicy)
	authedRouter.Delete("/api/v1/repositories/{name}/license/policies/{id}", h.handleDeletePolicy)

	// Global policies (admin)
	authedRouter.Get("/api/v1/admin/license/policies", h.handleListGlobalPolicies)
	authedRouter.Post("/api/v1/admin/license/policies", h.handleCreateGlobalPolicy)
}

// handleScan scans an artifact for licenses and evaluates policies.
func (h *LicenseHandler) handleScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "scan licenses")
	if !ok {
		return
	}

	var body struct {
		ArtifactID string         `json:"artifact_id"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateArtifactID(body.ArtifactID); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	// Caller-supplied metadata would let a scan report whatever the caller
	// wants; licenses are derived from the stored artifact and its properties.
	if len(body.Metadata) > 0 {
		writeError(w, http.StatusBadRequest, "validation_failed",
			"metadata is not accepted; set the artifact's license properties instead")

		return
	}

	resolved, err := h.licenses.licenses(ctx, repo.ID, body.ArtifactID)
	if writeLicenseSourceError(w, err, "") {
		return
	}

	if err != nil {
		h.dbError(w, "scan licenses: db error reading artifact licenses", err)
		return
	}

	// Evaluate the policies on the licenses the artifact declares and record
	// the result; a violated blocking policy quarantines the artifact.
	scanResult, violations, err := licensepolicy.Record(ctx, h.db, h.log, repo.ID, body.ArtifactID, resolved.Licenses, resolved.Source)
	if err != nil {
		h.dbError(w, "scan licenses: db error recording scan result", err)
		return
	}

	policyViolations := make([]licensepolicy.Violation, 0, len(violations))
	policyViolations = append(policyViolations, violations...)

	writeJSON(w, http.StatusOK, map[string]any{
		"id":                  scanResult.ID,
		"repo_id":             scanResult.RepositoryID,
		"artifact_id":         scanResult.ArtifactID,
		"licenses":            scanResult.Licenses,
		"unresolved_licenses": nonNilStrings(scanResult.UnresolvedLicenses),
		"license_source":      scanResult.LicenseSource,
		"scan_status":         scanResult.ScanStatus,
		"scanned_at":          scanResult.ScannedAt,
		"created_at":          scanResult.CreatedAt,
		"updated_at":          scanResult.UpdatedAt,
		"quarantined":         scanResult.Quarantined,
		"quarantine_approved": scanResult.QuarantineApproved,
		"policy_violations":   policyViolations,
	})
}

// handleQuarantine quarantines or approves the artifact of one license scan
// result. An artifact quarantined by a blocking policy stays quarantined, also
// across re-scans of the same licenses, until it is approved here
// ({"quarantined": false}).
func (h *LicenseHandler) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "quarantine license scan")
	if !ok {
		return
	}

	var body struct {
		Quarantined bool `json:"quarantined"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if !requireUUID(w, id, "license scan") {
		return
	}

	if err := h.db.SetLicenseScanQuarantine(ctx, repo.ID, id, body.Quarantined); err != nil {
		if errors.Is(err, database.ErrLicenseScanNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "license scan not found")
			return
		}

		h.dbError(w, "quarantine license scan: db error", err)
		return
	}

	updated, err := h.db.GetLicenseScanResultByID(ctx, repo.ID, id)
	if err != nil {
		h.dbError(w, "quarantine license scan: db error fetching updated result", err)
		return
	}

	if updated == nil {
		writeError(w, http.StatusNotFound, "not_found", "license scan not found")
		return
	}

	writeJSON(w, http.StatusOK, scanResultToMap(updated))
}

// handleListScans lists all license scan results for a repository.
func (h *LicenseHandler) handleListScans(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "list license scans")
	if !ok {
		return
	}

	writeRepoList(w, r, h.repoAccess, repo, "list license scans", "scans", h.db.ListLicenseScanResults, scanResultToMap)
}

// handleGetScan gets a specific license scan result.
func (h *LicenseHandler) handleGetScan(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "get license scan")
	if !ok {
		return
	}

	result, ok := fetchInRepo(w, r, h.repoAccess, repo, "get license scan: db error", "license scan", h.db.GetLicenseScanResultByID)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, scanResultToMap(result))
}

// handleListPolicies lists all license policies for a repository.
func (h *LicenseHandler) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "list license policies")
	if !ok {
		return
	}

	writeRepoList(w, r, h.repoAccess, repo, "list license policies", "policies", h.db.ListLicensePolicies, licensePolicyToMap)
}

// handleCreatePolicy creates a new license policy.
func (h *LicenseHandler) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "create license policy")
	if !ok {
		return
	}

	var body struct {
		Name            string   `json:"name"`
		Description     string   `json:"description"`
		Action          string   `json:"action"`
		AllowedLicenses []string `json:"allowed_licenses"`
		DeniedLicenses  []string `json:"denied_licenses"`
		Enabled         bool     `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is required")
		return
	}

	if len(body.Name) > maxNameLen {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is too long")
		return
	}

	if body.Action == "" {
		body.Action = "warn"
	}

	if !isValidLicensePolicyAction(body.Action) {
		writeError(w, http.StatusBadRequest, "validation_failed", "action must be one of: warn, block, allow")
		return
	}

	policy := &models.LicensePolicy{
		RepositoryID:    &repo.ID,
		Name:            body.Name,
		Description:     body.Description,
		Action:          body.Action,
		AllowedLicenses: body.AllowedLicenses,
		DeniedLicenses:  body.DeniedLicenses,
		Enabled:         body.Enabled,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := h.db.CreateLicensePolicy(ctx, policy); err != nil {
		h.dbError(w, "create license policy: db error", err)
		return
	}

	writeJSON(w, http.StatusCreated, licensePolicyToMap(policy))
}

// licensePolicyPatch is the partial-update body of a license policy.
type licensePolicyPatch struct {
	Name            *string  `json:"name"`
	Description     *string  `json:"description"`
	Action          *string  `json:"action"`
	AllowedLicenses []string `json:"allowed_licenses"`
	DeniedLicenses  []string `json:"denied_licenses"`
	Enabled         *bool    `json:"enabled"`
}

// apply validates the patch and copies the provided fields onto policy. It
// returns a validation message, leaving policy unchanged beyond the fields
// applied before the failing one, exactly as the inline version did.
func (b *licensePolicyPatch) apply(policy *models.LicensePolicy) string {
	if b.Name != nil {
		if *b.Name == "" || len(*b.Name) > maxNameLen {
			return "name must be 1-255 characters"
		}

		policy.Name = *b.Name
	}

	if b.Description != nil {
		policy.Description = *b.Description
	}

	if b.Action != nil {
		if !isValidLicensePolicyAction(*b.Action) {
			return "action must be one of: warn, block, allow"
		}

		policy.Action = *b.Action
	}

	if b.AllowedLicenses != nil {
		policy.AllowedLicenses = b.AllowedLicenses
	}

	if b.DeniedLicenses != nil {
		policy.DeniedLicenses = b.DeniedLicenses
	}

	if b.Enabled != nil {
		policy.Enabled = *b.Enabled
	}

	return ""
}

// handleUpdatePolicy updates a license policy.
func (h *LicenseHandler) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	policy, ok := h.repoPolicy(w, r, "update license policy")
	if !ok {
		return
	}

	var body licensePolicyPatch
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := body.apply(policy); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	policy.UpdatedAt = time.Now()

	if err := h.db.UpdateLicensePolicy(ctx, policy); err != nil {
		h.dbError(w, "update license policy: db error", err)
		return
	}

	writeJSON(w, http.StatusOK, licensePolicyToMap(policy))
}

// handleDeletePolicy deletes a license policy.
func (h *LicenseHandler) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	policy, ok := h.repoPolicy(w, r, "delete license policy")
	if !ok {
		return
	}

	if err := h.db.DeleteLicensePolicy(r.Context(), policy.ID); err != nil {
		h.dbError(w, "delete license policy: db error", err)
		return
	}

	writeNoContent(w)
}

// repoPolicy resolves the license policy {id} of the repository {name} for an
// admin: 404 when it does not exist or is not that repository's. Global
// policies (RepositoryID == nil) are only manageable through the admin
// endpoints, never through a repository-scoped URL.
func (h *LicenseHandler) repoPolicy(w http.ResponseWriter, r *http.Request, op string) (*models.LicensePolicy, bool) {
	repo, ok := h.adminRepo(w, r, op)
	if !ok {
		return nil, false
	}

	policy, ok := fetchByID(w, h.repoAccess, op+": db error getting policy", "license policy", urlparams.Vars(r)["id"],
		func(id string) (*models.LicensePolicy, error) { return h.db.GetLicensePolicy(r.Context(), id) })
	if !ok {
		return nil, false
	}

	if policy.RepositoryID == nil || *policy.RepositoryID != repo.ID {
		writeError(w, http.StatusNotFound, "not_found", "license policy not found")
		return nil, false
	}

	return policy, true
}

// handleListGlobalPolicies lists all global license policies.
func (h *LicenseHandler) handleListGlobalPolicies(w http.ResponseWriter, r *http.Request) {
	if !h.adminGate(w, r) {
		return
	}

	policies, err := h.db.ListGlobalLicensePolicies(r.Context())
	if err != nil {
		h.dbError(w, "list global license policies: db error", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"policies": mapList(policies, licensePolicyToMap),
	})
}

// handleCreateGlobalPolicy creates a new global license policy.
func (h *LicenseHandler) handleCreateGlobalPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !h.adminGate(w, r) {
		return
	}

	var body struct {
		Name            string   `json:"name"`
		Description     string   `json:"description"`
		Action          string   `json:"action"`
		AllowedLicenses []string `json:"allowed_licenses"`
		DeniedLicenses  []string `json:"denied_licenses"`
		Enabled         bool     `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is required")
		return
	}

	if len(body.Name) > maxNameLen {
		writeError(w, http.StatusBadRequest, "validation_failed", "name is too long")
		return
	}

	if body.Action == "" {
		body.Action = "warn"
	}

	if !isValidLicensePolicyAction(body.Action) {
		writeError(w, http.StatusBadRequest, "validation_failed", "action must be one of: warn, block, allow")
		return
	}

	policy := &models.LicensePolicy{
		RepositoryID:    nil, // Global policy
		Name:            body.Name,
		Description:     body.Description,
		Action:          body.Action,
		AllowedLicenses: body.AllowedLicenses,
		DeniedLicenses:  body.DeniedLicenses,
		Enabled:         body.Enabled,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := h.db.CreateLicensePolicy(ctx, policy); err != nil {
		h.dbError(w, "create global license policy: db error", err)
		return
	}

	writeJSON(w, http.StatusCreated, licensePolicyToMap(policy))
}

// Helper functions

func scanResultToMap(result *models.LicenseScanResult) responses.LicenseScanResultResponse {
	var scanError *string
	if result.ScanError != "" {
		scanError = &result.ScanError
	}

	return responses.LicenseScanResultResponse{
		ID:              result.ID,
		RepositoryID:    result.RepositoryID,
		ArtifactID:      result.ArtifactID,
		Licenses:        result.Licenses,
		ScanStatus:      result.ScanStatus,
		CreatedAt:       formatTime(result.CreatedAt),
		UpdatedAt:       formatTime(result.UpdatedAt),
		ScannedAt:       formatTimePtr(result.ScannedAt),
		ScanError:       scanError,
		SPDXExpressions: result.SPDXExpressions,

		UnresolvedLicenses: nonNilStrings(result.UnresolvedLicenses),
		LicenseSource:      result.LicenseSource,
		Quarantined:        result.Quarantined,
		QuarantineApproved: result.QuarantineApproved,
	}
}

func licensePolicyToMap(policy *models.LicensePolicy) responses.LicensePolicyResponse {
	description := policy.Description
	action := policy.Action

	return responses.LicensePolicyResponse{
		ID:              policy.ID,
		Name:            policy.Name,
		Description:     &description,
		Action:          &action,
		AllowedLicenses: policy.AllowedLicenses,
		DeniedLicenses:  policy.DeniedLicenses,
		Enabled:         policy.Enabled,
		CreatedAt:       formatTime(policy.CreatedAt),
		UpdatedAt:       formatTime(policy.UpdatedAt),
		RepositoryID:    policy.RepositoryID,
	}
}
