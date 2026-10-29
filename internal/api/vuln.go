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
	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/exploit"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/internal/workq"
	"github.com/openartifact/openartifact/pkg/logger"
)

// VulnHandler handles vulnerability scanning endpoints.
type VulnHandler struct {
	repoAccess

	// scanSem bounds the number of concurrently running background scans.
	scanSem chan struct{}

	// scanners builds the scanner backends and reports their availability.
	scanners *vuln.Registry

	// newBackend builds the scanner backend for a scanner name; tests replace it.
	newBackend func(name string) vuln.ScannerBackend

	// osvMirror is the mirror configuration reported by the status endpoint.
	osvMirror config.OSVMirrorConfig

	// onScanned, when set, is told about every scan that produced results.
	onScanned func(repoID, artifactID string)

	// tasks runs the background scans; they are cancelled at server shutdown.
	tasks *bgtask.Runner

	// exploit marks findings with the KEV and EPSS signals; never nil.
	exploit *exploit.Enricher

	// fwd, when set (server.role=web), receives the scans for a worker process
	// to run (ForwardScansTo).
	fwd *workq.Store
}

// WithExploit sets the enricher that marks findings with the CISA KEV and
// FIRST EPSS signals (the default has no feeds: findings carry their severity
// as priority).
func (h *VulnHandler) WithExploit(e *exploit.Enricher) *VulnHandler {
	h.exploit = e
	return h
}

// WithTasks runs the background scans under the server lifecycle: they are
// cancelled and awaited when the runner shuts down.
func (h *VulnHandler) WithTasks(tasks *bgtask.Runner) *VulnHandler {
	h.tasks = tasks
	return h
}

// WithScanHook sets a function called after each completed (or partial) scan
// was stored; the Dependency-Track integration uses it to push fresh results.
func (h *VulnHandler) WithScanHook(f func(repoID, artifactID string)) *VulnHandler {
	h.onScanned = f
	return h
}

// WithOSVMirror tells the handler how the local OSV mirror is configured, for
// the status endpoint.
func (h *VulnHandler) WithOSVMirror(cfg config.OSVMirrorConfig) *VulnHandler {
	h.osvMirror = cfg
	return h
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
		repoAccess: newRepoAccess(db, log, evaluator),
		scanSem:    make(chan struct{}, maxConcurrentVulnScans),
		scanners:   scanners,
		newBackend: scanners.Backend,
		tasks:      bgtask.New(context.Background()),
		exploit:    exploit.NewEnricher(nil, exploit.DefaultEPSSThreshold),
	}
}

// RegisterRoutes registers vulnerability scanning routes.
func (h *VulnHandler) RegisterRoutes(authedRouter chi.Router, pathPrefix string) {
	authedRouter.Post(pathPrefix+"/vuln/scan", h.handleScan)
	authedRouter.Get(pathPrefix+"/vuln/scanners", h.handleListScanners)
	authedRouter.Get(pathPrefix+"/vuln/scans", h.handleListScans)
	authedRouter.Get(pathPrefix+"/vuln/scans/{id}", h.handleGetScan)
	authedRouter.Get(pathPrefix+"/vuln/scans/{id}/export", h.handleExportScan)
	authedRouter.Get(pathPrefix+"/vuln/scans/{id}/gate", h.handleGateScan)
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
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "scan vuln")
	if !ok {
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

	// Grype needs the distribution of a deb or rpm; when it is unknown osv
	// scans instead, and the stored scan names the scanners that really run.
	pkg, scanners, err := h.scanPlan(ctx, repo, body.ArtifactID,
		vuln.Package{Name: body.PackageName, Version: body.PackageVersion, Ecosystem: body.Ecosystem}, scanners)
	if err != nil {
		h.log.Errorw("scan vuln: db error resolving the package distribution", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	// Reserve a background-scan slot before creating any state, so a flood of
	// requests cannot spawn unbounded goroutines or leave rows stuck "scanning".
	// A handler that forwards its scans runs none, so it has no slot.
	if !h.reserveScanSlot() {
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
		h.releaseScanSlot()

		h.log.Errorw("scan vuln: db error creating scan result", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if !h.startScan(ctx, w, repo.ID, scanResult, pkg) {
		return
	}

	resp, err := h.scanResponse(ctx, scanResult)
	if err != nil {
		h.dbError(w, "scan vuln: exploit signals", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// reserveScanSlot reserves a background-scan slot; it is false when all are
// taken.
func (h *VulnHandler) reserveScanSlot() bool {
	if h.fwd != nil {
		return true
	}

	select {
	case h.scanSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *VulnHandler) releaseScanSlot() {
	if h.fwd == nil {
		<-h.scanSem
	}
}

// startScan runs the scan of the stored result in the background, or hands it
// to a worker process. It answers the request and returns false when it could
// not.
func (h *VulnHandler) startScan(
	ctx context.Context,
	w http.ResponseWriter,
	repoID string,
	scanResult *models.VulnScanResult,
	pkg vuln.Package,
) bool {
	if h.fwd != nil {
		if err := h.forwardScan(ctx, scanResult, pkg); err != nil {
			// A row nobody will scan would stay "scanning" for ever.
			if delErr := h.db.DeleteVulnScanResult(context.WithoutCancel(ctx), repoID, scanResult.ID); delErr != nil {
				h.log.Errorw("scan vuln: removing the scan that could not be queued", "error", delErr)
			}

			h.dbError(w, "scan vuln: queueing the scan for the worker", err)

			return false
		}

		return true
	}

	// The goroutine owns its own copy: the handler keeps reading scanResult
	// while the scan mutates its result, which would be a data race.
	job := *scanResult

	h.tasks.Go(func(ctx context.Context) {
		defer h.releaseScanSlot()

		h.performScan(ctx, &job, pkg)
	})

	return true
}

// performScan performs the actual vulnerability scan of pkg with the scanners
// the result names.
func (h *VulnHandler) performScan(ctx context.Context, scanResult *models.VulnScanResult, pkg vuln.Package) {
	// ctx is the server lifecycle context: the scan ends with the server, and
	// the result below is still stored as failed.
	ctx, cancel := context.WithTimeout(ctx, vulnScanTimeout)
	defer cancel()

	// The scanner field holds one name or several joined by ",".
	names := strings.Split(scanResult.Scanner, ",")
	backends := make([]vuln.ScannerBackend, 0, len(names))

	for _, name := range names {
		backends = append(backends, h.newBackend(name))
	}

	// A scanner that cannot run is a failed scan, never "no vulnerabilities".
	// Run logs the failure details; only the sanitized text is stored.
	outcome := vuln.Run(ctx, h.log, backends, pkg)

	scanResult.ScanStatus = outcome.Status
	scanResult.ScanError = outcome.PublicError()
	scanResult.FailedScanners = outcome.FailedScanners()
	scanResult.Vulnerabilities = outcome.Vulnerabilities
	scanResult.VulnCount = len(outcome.Vulnerabilities)

	scanResult.CriticalCount, scanResult.HighCount, scanResult.MediumCount, scanResult.LowCount = vuln.CountSeverities(
		outcome.Vulnerabilities,
	)

	now := time.Now()
	scanResult.ScannedAt = &now
	scanResult.UpdatedAt = now

	// Persist with a fresh context: the scan context may already have expired
	// (scanner timeout) or been cancelled (shutdown), which would otherwise
	// leave the row stuck "scanning".
	persistCtx, persistCancel := context.WithTimeout(context.Background(), vulnPersistTimeout)
	defer persistCancel()

	// The aggregates are as of this scan; a failed read leaves zeros, which the
	// next feed import corrects.
	if aggs, err := h.exploit.Aggregates(persistCtx, outcome.Vulnerabilities); err != nil {
		h.log.Errorw("vuln scan: reading exploit signals", "error", err)
	} else if len(aggs) == 1 {
		scanResult.KEVCount, scanResult.MaxEPSS = aggs[0].KEVCount, aggs[0].MaxEPSS
	}

	if err := h.db.UpdateVulnScanResult(persistCtx, scanResult); err != nil {
		h.log.Errorw("vuln scan: db error updating scan result", "error", err)
		return
	}

	if h.onScanned != nil && (scanResult.ScanStatus == "completed" || scanResult.ScanStatus == "partial") {
		h.onScanned(scanResult.RepositoryID, scanResult.ArtifactID)
	}
}

// handleListScans lists all vulnerability scan results for a repository.
func (h *VulnHandler) handleListScans(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "list vuln scans")
	if !ok {
		return
	}

	vulnerableOnly := r.URL.Query().Get("vulnerable") == "true"

	results, err := h.db.ListVulnScanResults(r.Context(), repo.ID, vulnerableOnly)
	if err != nil {
		h.dbError(w, "list vuln scans: db error", err)
		return
	}

	out, err := h.scanResponses(r.Context(), results)
	if err != nil {
		h.dbError(w, "list vuln scans: exploit signals", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"scans": out})
}

// handleGetScan gets a specific vulnerability scan result.
func (h *VulnHandler) handleGetScan(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "get vuln scan")
	if !ok {
		return
	}

	result, ok := fetchInRepo(w, r, h.repoAccess, repo, "get vuln scan: db error", "vuln scan", h.db.GetVulnScanResultByID)
	if !ok {
		return
	}

	resp, err := h.scanResponse(r.Context(), result)
	if err != nil {
		h.dbError(w, "get vuln scan: exploit signals", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleQuarantine updates the quarantine status of a vulnerable artifact.
func (h *VulnHandler) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "quarantine vuln artifact")
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

	if !requireUUID(w, id, "vuln scan") {
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

	resp, err := h.scanResponse(ctx, updated)
	if err != nil {
		h.dbError(w, "quarantine vuln artifact: exploit signals", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleDeleteScan deletes a vulnerability scan result.
func (h *VulnHandler) handleDeleteScan(w http.ResponseWriter, r *http.Request) {
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	repo, ok := h.adminRepo(w, r, "delete vuln scan")
	if !ok {
		return
	}

	if !requireUUID(w, id, "vuln scan") {
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

// vulnScanResultToMap converts a stored scan result to its response, with the
// annotated findings (nil: none) and the priority derived at threshold.
func vulnScanResultToMap(
	result *models.VulnScanResult,
	findings []exploit.Vulnerability,
	threshold float64,
) responses.VulnScanResultResponse {
	var scanError *string
	if result.ScanError != "" {
		scanError = &result.ScanError
	}

	failedScanners := result.FailedScanners
	if failedScanners == nil {
		failedScanners = []string{}
	}

	if findings == nil {
		findings = []exploit.Vulnerability{}
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
		Vulnerabilities: findings,
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
		KEVCount:        result.KEVCount,
		MaxEPSS:         result.MaxEPSS,
		Priority: string(vulnRowPriority(
			result.KEVCount, result.MaxEPSS, result.CriticalCount, result.HighCount, result.MediumCount, result.LowCount, threshold)),
	}
}
