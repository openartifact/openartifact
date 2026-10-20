// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/pkg/logger"
)

// VulnHandler handles vulnerability scanning endpoints.
type VulnHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator

	// scanSem bounds the number of concurrently running background scans.
	scanSem chan struct{}
}

const (
	// maxConcurrentVulnScans bounds background scan goroutines per handler.
	maxConcurrentVulnScans = 4

	// vulnScanTimeout bounds the scanner backend call.
	vulnScanTimeout = 120 * time.Second

	// vulnPersistTimeout bounds the final DB write, independent of the scan context.
	vulnPersistTimeout = 30 * time.Second
)

func isValidVulnScanner(scanner string) bool {
	switch scanner {
	case "osv", "trivy", "grype":
		return true
	}

	return false
}

// NewVulnHandler creates a new vulnerability handler.
func NewVulnHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator) *VulnHandler {
	return &VulnHandler{
		db:        db,
		log:       log,
		evaluator: evaluator,
		scanSem:   make(chan struct{}, maxConcurrentVulnScans),
	}
}

// RegisterRoutes registers vulnerability scanning routes.
func (h *VulnHandler) RegisterRoutes(authedRouter chi.Router, pathPrefix string) {
	authedRouter.Post(pathPrefix+"/vuln/scan", h.handleScan)
	authedRouter.Get(pathPrefix+"/vuln/scans", h.handleListScans)
	authedRouter.Get(pathPrefix+"/vuln/scans/{id}", h.handleGetScan)
	authedRouter.Post(pathPrefix+"/vuln/scans/{id}/quarantine", h.handleQuarantine)
	authedRouter.Delete(pathPrefix+"/vuln/scans/{id}", h.handleDeleteScan)
}

// handleScan initiates a vulnerability scan for a package.
func (h *VulnHandler) handleScan(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("scan vuln: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	var body struct {
		ArtifactID     string `json:"artifact_id"`
		PackageName    string `json:"package_name"`
		PackageVersion string `json:"package_version"`
		Ecosystem      string `json:"ecosystem"`
		Scanner        string `json:"scanner"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateArtifactID(body.ArtifactID); msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	if body.PackageName == "" || body.PackageVersion == "" || body.Ecosystem == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "artifact_id, package_name, package_version, and ecosystem are required")
		return
	}

	if len(body.PackageName) > maxArtifactIDLen || len(body.PackageVersion) > maxNameLen || len(body.Ecosystem) > maxNameLen {
		writeError(w, http.StatusBadRequest, "validation_failed", "package_name, package_version or ecosystem is too long")
		return
	}

	body.Scanner = strings.ToLower(body.Scanner)
	if body.Scanner == "" {
		body.Scanner = "osv"
	}

	if !isValidVulnScanner(body.Scanner) {
		writeError(w, http.StatusBadRequest, "validation_failed", "scanner must be one of: osv, trivy, grype")
		return
	}

	// Reserve a background-scan slot before creating any state, so a flood of
	// requests cannot spawn unbounded goroutines or leave rows stuck "scanning".
	select {
	case h.scanSem <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "too_many_scans", "too many vulnerability scans in progress")

		return
	}

	// Create initial scan result with "scanning" status
	now := time.Now()
	scanResult := &models.VulnScanResult{
		RepositoryID:    repo.ID,
		ArtifactID:      body.ArtifactID,
		PackageName:     body.PackageName,
		PackageVersion:  body.PackageVersion,
		Ecosystem:       body.Ecosystem,
		Scanner:         body.Scanner,
		ScanStatus:      "scanning",
		Vulnerabilities: []models.Vulnerability{},
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := h.db.CreateVulnScanResult(ctx, scanResult); err != nil {
		<-h.scanSem

		h.log.Errorw("scan vuln: db error creating scan result", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// The goroutine owns its own copy: the handler keeps reading scanResult
	// below while the scan mutates its result, which would be a data race.
	job := *scanResult

	// Run scan asynchronously
	go func() {
		defer func() { <-h.scanSem }()

		h.performScan(&job)
	}()

	writeJSON(w, http.StatusOK, vulnScanResultToMap(scanResult))
}

// performScan performs the actual vulnerability scan.
func (h *VulnHandler) performScan(scanResult *models.VulnScanResult) {
	// Use background context for async operation
	ctx, cancel := context.WithTimeout(context.Background(), vulnScanTimeout)
	defer cancel()

	// Create scanner backend based on scanner field
	backend := vuln.NewScannerForBackend(scanResult.Scanner, h.log)

	// Query the vulnerability scanner
	vulns, err := backend.Scan(ctx, scanResult.PackageName, scanResult.PackageVersion, scanResult.Ecosystem)
	if err != nil {
		h.log.Errorw("vuln scan failed", "error", err, "package", scanResult.PackageName)
		scanResult.ScanStatus = "failed"
		scanResult.ScanError = err.Error()
	} else {
		scanResult.ScanStatus = "completed"
		scanResult.Vulnerabilities = vulns
		scanResult.VulnCount = len(vulns)

		// Count by severity
		for _, v := range vulns {
			switch strings.ToUpper(v.Severity) {
			case "CRITICAL":
				scanResult.CriticalCount++
			case "HIGH":
				scanResult.HighCount++
			case "MEDIUM":
				scanResult.MediumCount++
			case "LOW":
				scanResult.LowCount++
			}
		}
	}

	now := time.Now()
	scanResult.ScannedAt = &now
	scanResult.UpdatedAt = now

	// Persist with a fresh context: the scan context may already have expired
	// (scanner timeout), which would otherwise leave the row stuck "scanning".
	persistCtx, persistCancel := context.WithTimeout(context.Background(), vulnPersistTimeout)
	defer persistCancel()

	if err := h.db.UpdateVulnScanResult(persistCtx, scanResult); err != nil {
		h.log.Errorw("vuln scan: db error updating scan result", "error", err)
	}
}

// handleListScans lists all vulnerability scan results for a repository.
func (h *VulnHandler) handleListScans(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("list vuln scans: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	vulnerableOnly := r.URL.Query().Get("vulnerable") == "true"

	results, err := h.db.ListVulnScanResults(ctx, repo.ID, vulnerableOnly)
	if err != nil {
		h.log.Errorw("list vuln scans: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if results == nil {
		results = []*models.VulnScanResult{}
	}

	var resultMaps []responses.VulnScanResultResponse
	for _, result := range results {
		resultMaps = append(resultMaps, vulnScanResultToMap(result))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scans": resultMaps,
	})
}

// handleGetScan gets a specific vulnerability scan result.
//
//nolint:dupl // handler boilerplate: identical structure, different entities
func (h *VulnHandler) handleGetScan(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("get vuln scan: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
		return
	}

	result, err := h.db.GetVulnScanResultByID(ctx, repo.ID, id)
	if err != nil {
		h.log.Errorw("get vuln scan: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if result == nil {
		writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
		return
	}

	writeJSON(w, http.StatusOK, vulnScanResultToMap(result))
}

// handleQuarantine updates the quarantine status of a vulnerable artifact.
func (h *VulnHandler) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("quarantine vuln artifact: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
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

	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
		return
	}

	// Quarantine exactly the addressed scan result; the artifact-wide helper
	// would fail (multiple rows) when several scanners scanned the artifact.
	if err := h.db.SetVulnScanQuarantine(ctx, repo.ID, id, body.Quarantined); err != nil {
		if errors.Is(err, database.ErrVulnScanNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
			return
		}

		h.log.Errorw("quarantine vuln artifact: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Fetch updated result
	updated, err := h.db.GetVulnScanResultByID(ctx, repo.ID, id)
	if err != nil {
		h.log.Errorw("quarantine vuln artifact: db error fetching updated result", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if updated == nil {
		writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
		return
	}

	writeJSON(w, http.StatusOK, vulnScanResultToMap(updated))
}

// handleDeleteScan deletes a vulnerability scan result.
func (h *VulnHandler) handleDeleteScan(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("delete vuln scan: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return
	}

	p := currentPrincipal(r)

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil || !isAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
		return
	}

	if err := h.db.DeleteVulnScanResult(ctx, repo.ID, id); err != nil {
		if errors.Is(err, database.ErrVulnScanNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
			return
		}

		h.log.Errorw("delete vuln scan: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeNoContent(w)
}

// Helper function to convert VulnScanResult to map.
func vulnScanResultToMap(result *models.VulnScanResult) responses.VulnScanResultResponse {
	var scanError *string
	if result.ScanError != "" {
		scanError = &result.ScanError
	}

	return responses.VulnScanResultResponse{
		ID:              result.ID,
		RepositoryID:    result.RepositoryID,
		ArtifactID:      result.ArtifactID,
		PackageName:     result.PackageName,
		PackageVersion:  result.PackageVersion,
		Ecosystem:       result.Ecosystem,
		Scanner:         result.Scanner,
		ScanStatus:      result.ScanStatus,
		Vulnerabilities: result.Vulnerabilities,
		VulnCount:       result.VulnCount,
		CriticalCount:   result.CriticalCount,
		HighCount:       result.HighCount,
		MediumCount:     result.MediumCount,
		LowCount:        result.LowCount,
		Quarantined:     result.Quarantined,
		CreatedAt:       formatTime(result.CreatedAt),
		UpdatedAt:       formatTime(result.UpdatedAt),
		ScannedAt:       formatTimePtr(result.ScannedAt),
		ScanError:       scanError,
	}
}
