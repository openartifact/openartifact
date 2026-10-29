// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
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

// SecretsStore is the part of the database SecretsHandler needs.
type SecretsStore interface {
	repoAccessStore
	CreateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error
	DeleteSecretScanResult(ctx context.Context, repoID string, id string) error
	GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error)
	GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error)
	GetProjectByName(ctx context.Context, name string) (*models.Project, error)
	GetSecretScanResult(ctx context.Context, repoID string, artifactID string) (*models.SecretScanResult, error)
	ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error)
	ListPublicRepositoryNames(ctx context.Context) ([]string, error)
	ListSecretScanResults(ctx context.Context, repoID string, flaggedOnly bool) ([]*models.SecretScanResult, error)
	QuarantineArtifact(ctx context.Context, repoID string, artifactID string, quarantined bool) error
}

type SecretsHandler struct {
	repoAccess

	scanner *secrets.Scanner
	db      SecretsStore
}

func NewSecretsHandler(db SecretsStore, log *logger.Logger, scanner *secrets.Scanner) *SecretsHandler {
	return &SecretsHandler{
		repoAccess: newRepoAccess(db, log, auth.NewEvaluator(db, log, false)),
		scanner:    scanner, db: db,
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
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "scan secrets")
	if !ok {
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
	repo, ok := h.adminRepo(w, r, "list secret scans")
	if !ok {
		return
	}

	flaggedOnly := r.URL.Query().Get("flagged") == "true"

	writeRepoList(w, r, h.repoAccess, repo, "list secret scans", "scans",
		func(ctx context.Context, repoID string) ([]*models.SecretScanResult, error) {
			return h.db.ListSecretScanResults(ctx, repoID, flaggedOnly)
		}, secretScanResultToMap)
}

// handleGetScan gets a specific secret scan result.
func (h *SecretsHandler) handleGetScan(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "get secret scan")
	if !ok {
		return
	}

	result, ok := h.findScan(w, r, repo, "get secret scan: db error")
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, secretScanResultToMap(result))
}

// handleQuarantine updates the quarantine status of an artifact.
func (h *SecretsHandler) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "quarantine artifact")
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

	result, ok := h.findScan(w, r, repo, "quarantine artifact: db error listing scans")
	if !ok {
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

// findScan finds the scan result {id} among the repository's secret scans; a
// listing failure is logged as logMsg, an unknown id is a 404.
func (h *SecretsHandler) findScan(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	logMsg string,
) (*models.SecretScanResult, bool) {
	results, err := h.db.ListSecretScanResults(r.Context(), repo.ID, false)
	if err != nil {
		h.dbError(w, logMsg, err)
		return nil, false
	}

	id := urlparams.Vars(r)["id"]

	for _, res := range results {
		if res.ID == id {
			return res, true
		}
	}

	writeError(w, http.StatusNotFound, "not_found", "secret scan not found")

	return nil, false
}

// handleDeleteScan deletes a secret scan result.
func (h *SecretsHandler) handleDeleteScan(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "delete secret scan")
	if !ok {
		return
	}

	id := urlparams.Vars(r)["id"]
	if !requireUUID(w, id, "secret scan") {
		return
	}

	if err := h.db.DeleteSecretScanResult(r.Context(), repo.ID, id); err != nil {
		if errors.Is(err, database.ErrSecretScanNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "secret scan not found")
			return
		}

		h.dbError(w, "delete secret scan: db error", err)

		return
	}

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

	if !requireAdmin(w, r, h.evaluator) {
		return nil
	}

	format, valid := exportFormat(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be one of: sarif, csv, json")
		return nil
	}

	repo, ok := h.repoNamed(w, r, "export secret scans: db error getting repo")
	if !ok {
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
			writeSecretsExport(
				w,
				ex.format,
				exportFileName(ex.format, "secrets", ex.repoName, id),
				ex.repoName,
				[]*models.SecretScanResult{res},
			)
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
