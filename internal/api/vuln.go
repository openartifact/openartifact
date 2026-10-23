// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
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

	// scanners builds the scanner backends and reports their availability.
	scanners *vuln.Registry

	// newBackend builds the scanner backend for a scanner name; tests replace it.
	newBackend func(name string) vuln.ScannerBackend
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

// normalizeVulnScanners merges the request's scanner and scanners into a
// sorted, duplicate-free list (default: osv). ok is false when any name is not
// a known scanner.
func normalizeVulnScanners(single string, list []string) ([]string, bool) {
	names := make([]string, 0, len(list)+1)
	if single != "" {
		names = append(names, single)
	}

	names = append(names, list...)

	if len(names) == 0 {
		return []string{"osv"}, true
	}

	for i, n := range names {
		names[i] = strings.ToLower(strings.TrimSpace(n))
		if !isValidVulnScanner(names[i]) {
			return nil, false
		}
	}

	slices.Sort(names)

	return slices.Compact(names), true
}

// NewVulnHandler creates a new vulnerability handler. scanners provides the
// scanner backends and their availability; nil means the defaults (trivy and
// grype from PATH, no database refresh).
func NewVulnHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, scanners *vuln.Registry) *VulnHandler {
	if scanners == nil {
		scanners = vuln.NewRegistry(log, vuln.Options{})
	}

	return &VulnHandler{
		db:         db,
		log:        log,
		evaluator:  evaluator,
		scanSem:    make(chan struct{}, maxConcurrentVulnScans),
		scanners:   scanners,
		newBackend: scanners.Backend,
	}
}

// RegisterRoutes registers vulnerability scanning routes.
func (h *VulnHandler) RegisterRoutes(authedRouter chi.Router, pathPrefix string) {
	authedRouter.Post(pathPrefix+"/vuln/scan", h.handleScan)
	authedRouter.Get(pathPrefix+"/vuln/scanners", h.handleListScanners)
	authedRouter.Get(pathPrefix+"/vuln/scans", h.handleListScans)
	authedRouter.Get(pathPrefix+"/vuln/scans/{id}", h.handleGetScan)
	authedRouter.Get(pathPrefix+"/vuln/scans/{id}/export", h.handleExportScan)
	authedRouter.Post(pathPrefix+"/vuln/scans/{id}/quarantine", h.handleQuarantine)
	authedRouter.Delete(pathPrefix+"/vuln/scans/{id}", h.handleDeleteScan)
}

// handleListScanners reports which vulnerability scanners can run on this
// server, so clients can disable the ones that cannot and say why.
func (h *VulnHandler) handleListScanners(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	statuses := h.scanners.Status()
	out := make([]responses.VulnScannerResponse, 0, len(statuses))

	for _, s := range statuses {
		out = append(out, vulnScannerToResponse(s))
	}

	writeJSON(w, http.StatusOK, map[string]any{"scanners": out})
}

func vulnScannerToResponse(s vuln.ScannerStatus) responses.VulnScannerResponse {
	resp := responses.VulnScannerResponse{
		Name:      s.Name,
		Available: s.Available,
		Ready:     s.Ready,
		DBState:   nonEmptyString(s.DBState),
		Code:      nonEmptyString(s.Code),
		Message:   nonEmptyString(s.Message),
		DocsURL:   nonEmptyString(s.DocsURL),

		InstallState: nonEmptyString(s.InstallState),
	}

	if !s.DBUpdatedAt.IsZero() {
		at := s.DBUpdatedAt.UTC().Format(time.RFC3339)
		resp.DBUpdatedAt = &at
	}

	return resp
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
		ArtifactID     string   `json:"artifact_id"`
		PackageName    string   `json:"package_name"`
		PackageVersion string   `json:"package_version"`
		Ecosystem      string   `json:"ecosystem"`
		Scanner        string   `json:"scanner"`
		Scanners       []string `json:"scanners"`
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

	scanners, ok := normalizeVulnScanners(body.Scanner, body.Scanners)
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "scanner and scanners must be among: osv, trivy, grype")
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
		Scanner:         strings.Join(scanners, ","),
		ScanStatus:      "scanning",
		Vulnerabilities: []models.Vulnerability{},
		FailedScanners:  []string{},
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

	// The scanner field holds one name or several joined by ",".
	names := strings.Split(scanResult.Scanner, ",")
	backends := make([]vuln.ScannerBackend, 0, len(names))

	for _, name := range names {
		backends = append(backends, h.newBackend(name))
	}

	// A scanner that cannot run is a failed scan, never "no vulnerabilities".
	// Run logs the failure details; only the sanitized text is stored.
	outcome := vuln.Run(ctx, h.log, backends, scanResult.PackageName, scanResult.PackageVersion, scanResult.Ecosystem)

	scanResult.ScanStatus = outcome.Status
	scanResult.ScanError = outcome.PublicError()
	scanResult.FailedScanners = outcome.FailedScanners()
	scanResult.Vulnerabilities = outcome.Vulnerabilities
	scanResult.VulnCount = len(outcome.Vulnerabilities)

	for _, v := range outcome.Vulnerabilities {
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

	failedScanners := result.FailedScanners
	if failedScanners == nil {
		failedScanners = []string{}
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
		FailedScanners:  failedScanners,
	}
}
