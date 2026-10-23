// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/scanexport"
	"github.com/openartifact/openartifact/internal/secrets"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

type SecretsHandler struct {
	db      database.Database
	log     *logger.Logger
	scanner *secrets.Scanner
}

func NewSecretsHandler(db database.Database, log *logger.Logger, scanner *secrets.Scanner) *SecretsHandler {
	return &SecretsHandler{
		db:      db,
		log:     log,
		scanner: scanner,
	}
}

func (h *SecretsHandler) RegisterRoutes(authedRouter chi.Router, pathPrefix string) {
	authedRouter.Post(pathPrefix+"/secrets/scan", h.handleScan)
	authedRouter.Get(pathPrefix+"/secrets/scans", h.handleListScans)
	authedRouter.Get(pathPrefix+"/secrets/scans/export", h.handleExportScans)
	authedRouter.Get(pathPrefix+"/secrets/scans/{id}", h.handleGetScan)
	authedRouter.Get(pathPrefix+"/secrets/scans/{id}/export", h.handleExportScan)
	authedRouter.Post(pathPrefix+"/secrets/scans/{id}/quarantine", h.handleQuarantine)
	authedRouter.Delete(pathPrefix+"/secrets/scans/{id}", h.handleDeleteScan)
}

// handleScan scans an artifact for secrets.
func (h *SecretsHandler) handleScan(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("scan secrets: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, auth.NewEvaluator(h.db, h.log, false))
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body struct {
		ArtifactID string         `json:"artifact_id"`
		Content    string         `json:"content"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.ArtifactID == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "artifact_id is required")
		return
	}

	// Scan content and metadata for secrets
	var findings []models.SecretFinding

	if body.Content != "" {
		findings = append(findings, h.scanner.ScanText(body.Content)...)
	}

	if len(body.Metadata) > 0 {
		findings = append(findings, h.scanner.ScanMetadata(body.Metadata)...)
	}

	// Determine scan status
	scanStatus := "clean"
	if len(findings) > 0 {
		scanStatus = "flagged"
	}

	// Create scan result
	now := time.Now()
	scanResult := &models.SecretScanResult{
		RepositoryID: repo.ID,
		ArtifactID:   body.ArtifactID,
		ScanStatus:   scanStatus,
		Findings:     findings,
		FindingCount: len(findings),
		Quarantined:  false,
		ScannedAt:    &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := h.db.CreateSecretScanResult(ctx, scanResult); err != nil {
		h.log.Errorw("scan secrets: db error creating scan result", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, secretScanResultToMap(scanResult))
}

// handleListScans lists all secret scan results for a repository.
func (h *SecretsHandler) handleListScans(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("list secret scans: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, auth.NewEvaluator(h.db, h.log, false))
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	flaggedOnly := r.URL.Query().Get("flagged") == "true"

	results, err := h.db.ListSecretScanResults(ctx, repo.ID, flaggedOnly)
	if err != nil {
		h.log.Errorw("list secret scans: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if results == nil {
		results = []*models.SecretScanResult{}
	}

	var resultMaps []responses.SecretScanResultResponse
	for _, result := range results {
		resultMaps = append(resultMaps, secretScanResultToMap(result))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scans": resultMaps,
	})
}

// handleGetScan gets a specific secret scan result.
func (h *SecretsHandler) handleGetScan(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get secret scan: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, auth.NewEvaluator(h.db, h.log, false))
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	// Get all scans and find the one with matching ID
	results, err := h.db.ListSecretScanResults(ctx, repo.ID, false)
	if err != nil {
		h.log.Errorw("get secret scan: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	var result *models.SecretScanResult

	for _, r := range results {
		if r.ID == id {
			result = r
			break
		}
	}

	if result == nil {
		writeError(w, http.StatusNotFound, "not_found", "secret scan not found")
		return
	}

	writeJSON(w, http.StatusOK, secretScanResultToMap(result))
}

// handleQuarantine updates the quarantine status of an artifact.
func (h *SecretsHandler) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("quarantine artifact: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, auth.NewEvaluator(h.db, h.log, false))
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body struct {
		Quarantined bool `json:"quarantined"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	// Get the scan result to find artifact ID
	results, err := h.db.ListSecretScanResults(ctx, repo.ID, false)
	if err != nil {
		h.log.Errorw("quarantine artifact: db error listing scans", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	var result *models.SecretScanResult

	for _, r := range results {
		if r.ID == id {
			result = r
			break
		}
	}

	if result == nil {
		writeError(w, http.StatusNotFound, "not_found", "secret scan not found")
		return
	}

	// Update quarantine status
	if err := h.db.QuarantineArtifact(ctx, repo.ID, result.ArtifactID, body.Quarantined); err != nil {
		h.log.Errorw("quarantine artifact: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Fetch updated result
	updated, err := h.db.GetSecretScanResult(ctx, repo.ID, result.ArtifactID)
	if err != nil {
		h.log.Errorw("quarantine artifact: db error fetching updated result", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, secretScanResultToMap(updated))
}

// handleDeleteScan deletes a secret scan result.
func (h *SecretsHandler) handleDeleteScan(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete secret scan: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, auth.NewEvaluator(h.db, h.log, false))
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	// Get all scans and find the one with matching ID
	results, err := h.db.ListSecretScanResults(ctx, repo.ID, false)
	if err != nil {
		h.log.Errorw("delete secret scan: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	var result *models.SecretScanResult

	for _, r := range results {
		if r.ID == id {
			result = r
			break
		}
	}

	if result == nil {
		writeError(w, http.StatusNotFound, "not_found", "secret scan not found")
		return
	}

	// The spec says DELETE returns 204.
	writeNoContent(w)
}

// Helper function to convert SecretScanResult to map.
func secretScanResultToMap(result *models.SecretScanResult) responses.SecretScanResultResponse {
	var scanError *string
	if result.ScanError != "" {
		scanError = &result.ScanError
	}

	return responses.SecretScanResultResponse{
		ID:           result.ID,
		RepositoryID: result.RepositoryID,
		ArtifactID:   result.ArtifactID,
		ScanStatus:   result.ScanStatus,
		Findings:     result.Findings,
		FindingCount: result.FindingCount,
		Quarantined:  result.Quarantined,
		CreatedAt:    formatTime(result.CreatedAt),
		UpdatedAt:    formatTime(result.UpdatedAt),
		ScannedAt:    formatTimePtr(result.ScannedAt),
		ScanError:    scanError,
	}
}

// secretExport is the data of a secret scan export request.
type secretExport struct {
	repoName string
	results  []*models.SecretScanResult
	format   string
}

// loadExport checks the admin flag and the format and loads the repository's
// secret scans; on failure it writes the error response and returns nil.
func (h *SecretsHandler) loadExport(w http.ResponseWriter, r *http.Request, flaggedOnly bool) *secretExport {
	ctx := r.Context()

	if !requireAdmin(w, r, auth.NewEvaluator(h.db, h.log, false)) {
		return nil
	}

	format, valid := exportFormat(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be one of: sarif, csv, json")
		return nil
	}

	repo, err := h.db.GetRepositoryByName(ctx, urlparams.Vars(r)["name"])
	if err != nil {
		h.log.Errorw("export secret scans: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil
	}

	results, err := h.db.ListSecretScanResults(ctx, repo.ID, flaggedOnly)
	if err != nil {
		h.log.Errorw("export secret scans: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil
	}

	return &secretExport{repoName: repo.Name, results: results, format: format}
}

// handleExportScans downloads the findings of all (or, with ?flagged=true, the
// flagged) secret scans of a repository as SARIF, CSV or JSON.
func (h *SecretsHandler) handleExportScans(w http.ResponseWriter, r *http.Request) {
	ex := h.loadExport(w, r, r.URL.Query().Get("flagged") == "true")
	if ex == nil {
		return
	}

	writeSecretsExport(w, ex.format, exportFileName(ex.format, "secrets", ex.repoName), ex.repoName, ex.results)
}

// handleExportScan downloads one secret scan's findings as SARIF, CSV or JSON.
func (h *SecretsHandler) handleExportScan(w http.ResponseWriter, r *http.Request) {
	ex := h.loadExport(w, r, false)
	if ex == nil {
		return
	}

	id := urlparams.Vars(r)["id"]

	for _, res := range ex.results {
		if res.ID == id {
			writeSecretsExport(w, ex.format, exportFileName(ex.format, "secrets", ex.repoName, id), ex.repoName, []*models.SecretScanResult{res})
			return
		}
	}

	writeError(w, http.StatusNotFound, "not_found", "secret scan not found")
}

func writeSecretsExport(w http.ResponseWriter, format, filename, repoName string, results []*models.SecretScanResult) {
	items := make([]scanexport.SecretItem, 0, len(results))
	for _, res := range results {
		items = append(items, scanexport.SecretItem{Repo: repoName, Scan: res})
	}

	switch format {
	case exportFormatSARIF:
		log, cut := scanexport.SecretsSARIF(items)
		exportHeaders(w, format, filename, cut)
		writeExportJSON(w, log)
	case exportFormatCSV:
		exportHeaders(w, format, filename, false)
		writeExportCSV(w, scanexport.SecretsCSVHeader, scanexport.SecretsCSVRows(items))
	default:
		exportHeaders(w, format, filename, false)

		// The stored findings carry a redacted value and a line hint; an export
		// holds neither.
		out := make([]responses.SecretScanExportResponse, 0, len(results))
		for _, res := range results {
			out = append(out, secretScanExport(res))
		}

		if len(results) == 1 {
			writeExportJSON(w, out[0])
			return
		}

		writeExportJSON(w, map[string]any{"scans": out})
	}
}

func secretScanExport(res *models.SecretScanResult) responses.SecretScanExportResponse {
	findings := make([]responses.SecretFindingExport, 0, len(res.Findings))
	for _, f := range res.Findings {
		findings = append(findings, responses.SecretFindingExport{Type: f.Type, Description: f.Description, Severity: f.Severity})
	}

	var scanError *string
	if res.ScanError != "" {
		scanError = &res.ScanError
	}

	return responses.SecretScanExportResponse{
		ID: res.ID, ArtifactID: res.ArtifactID, ScanStatus: res.ScanStatus,
		Findings: findings, FindingCount: res.FindingCount, Quarantined: res.Quarantined,
		CreatedAt: formatTime(res.CreatedAt), ScannedAt: formatTimePtr(res.ScannedAt), ScanError: scanError,
	}
}
