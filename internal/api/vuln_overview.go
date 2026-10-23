// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/scanexport"
	"github.com/openartifact/openartifact/internal/urlparams"
)

const (
	vulnOverviewDefaultLimit = 25
	vulnOverviewMaxLimit     = 100
)

// RegisterOverviewRoutes registers the cross-repository scan overview, which is
// not scoped to one repository. Administrative like every scan route: the
// RBAC middleware requires "manage" on /api/v1/ paths and the handlers check
// the admin flag again.
func (h *VulnHandler) RegisterOverviewRoutes(authedRouter chi.Router) {
	authedRouter.Get("/api/v1/vuln/scans", h.handleOverview)
	authedRouter.Get("/api/v1/vuln/scans/summary", h.handleOverviewSummary)
	authedRouter.Get("/api/v1/vuln/scans/export", h.handleExportOverview)
}

// vulnOverviewParams reads and validates the overview's query string. It
// returns the message of the first invalid value, or "".
func vulnOverviewParams(r *http.Request) (models.VulnScanListParams, string) {
	q := r.URL.Query()

	params := models.VulnScanListParams{
		Repo:     q.Get("repo"),
		Severity: q.Get("severity"),
		Status:   q.Get("status"),
		Scanner:  q.Get("scanner"),
		Limit:    queryIntParam(r, "limit", vulnOverviewDefaultLimit, 1, vulnOverviewMaxLimit),
	}

	if len(params.Repo) > maxNameLen {
		return params, "repo is too long"
	}

	switch params.Severity {
	case "", "critical", "high", "medium", "low":
	default:
		return params, "severity must be one of: critical, high, medium, low"
	}

	switch params.Status {
	case "", "completed", "partial", "failed", "scanning":
	default:
		return params, "status must be one of: completed, partial, failed, scanning"
	}

	switch params.Scanner {
	case "", "osv", "trivy", "grype":
	default:
		return params, "scanner must be one of: osv, trivy, grype"
	}

	switch q.Get("quarantined") {
	case "":
	case "true", "false":
		v := q.Get("quarantined") == "true"
		params.Quarantined = &v
	default:
		return params, "quarantined must be true or false"
	}

	if raw := q.Get("cursor"); raw != "" {
		cursor, err := decodeVulnCursor(raw)
		if err != nil {
			return params, "invalid cursor"
		}

		params.After = cursor
	}

	return params, ""
}

// handleOverview lists vulnerability scans across all repositories, worst
// first, one keyset page at a time and without the findings.
func (h *VulnHandler) handleOverview(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	params, msg := vulnOverviewParams(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	page, err := h.db.ListVulnScanOverview(r.Context(), params)
	if err != nil {
		if errors.Is(err, database.ErrInvalidVulnScanFilter) {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid filter")
			return
		}

		h.log.Errorw("vuln scan overview: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	resp := responses.VulnScanOverviewResponse{
		Scans:   make([]responses.VulnScanOverviewRowResponse, 0, len(page.Scans)),
		HasMore: page.Next != nil,
	}

	for _, s := range page.Scans {
		resp.Scans = append(resp.Scans, vulnOverviewRowToResponse(s))
	}

	if page.Next != nil {
		next := encodeVulnCursor(page.Next)
		resp.NextCursor = &next
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleOverviewSummary counts the overview's scans by status, quarantine and
// worst severity, for all repositories or the one named by ?repo.
func (h *VulnHandler) handleOverviewSummary(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	repo := r.URL.Query().Get("repo")
	if len(repo) > maxNameLen {
		writeError(w, http.StatusBadRequest, "validation_failed", "repo is too long")
		return
	}

	s, err := h.db.VulnScanOverviewSummary(r.Context(), repo)
	if err != nil {
		h.log.Errorw("vuln scan overview summary: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, responses.VulnScanSummaryResponse{
		Total:       s.Total,
		Completed:   s.Completed,
		Partial:     s.Partial,
		Failed:      s.Failed,
		Scanning:    s.Scanning,
		Quarantined: s.Quarantined,
		SeverityAtLeast: responses.VulnScanSeverityCounts{
			Critical: s.WithCritical,
			High:     s.WithHigh,
			Medium:   s.WithMedium,
			Low:      s.WithLow,
		},
	})
}

func vulnOverviewRowToResponse(s *models.VulnScanOverviewRow) responses.VulnScanOverviewRowResponse {
	var scanError *string
	if s.ScanError != "" {
		scanError = &s.ScanError
	}

	failedScanners := s.FailedScanners
	if failedScanners == nil {
		failedScanners = []string{}
	}

	return responses.VulnScanOverviewRowResponse{
		ID:               s.ID,
		RepositoryID:     s.RepositoryID,
		RepositoryName:   s.RepositoryName,
		RepositoryFormat: s.RepositoryFormat,
		ArtifactID:       s.ArtifactID,
		PackageName:      s.PackageName,
		PackageVersion:   s.PackageVersion,
		Ecosystem:        s.Ecosystem,
		Scanner:          s.Scanner,
		ScanStatus:       s.ScanStatus,
		VulnCount:        s.VulnCount,
		CriticalCount:    s.CriticalCount,
		HighCount:        s.HighCount,
		MediumCount:      s.MediumCount,
		LowCount:         s.LowCount,
		Quarantined:      s.Quarantined,
		CreatedAt:        formatTime(s.CreatedAt),
		UpdatedAt:        formatTime(s.UpdatedAt),
		ScannedAt:        formatTimePtr(s.ScannedAt),
		ScanError:        scanError,
		FailedScanners:   failedScanners,
	}
}

const (
	// vulnExportMaxScans bounds how many scans a cross-repository export reads
	// (one query each for the findings).
	vulnExportMaxScans = 2000
)

// handleExportScan downloads one scan's findings as SARIF, CSV or JSON.
func (h *VulnHandler) handleExportScan(w http.ResponseWriter, r *http.Request) {
	name := urlparams.Vars(r)["name"]
	id := urlparams.Vars(r)["id"]
	ctx := r.Context()

	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	format, ok := exportFormat(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be one of: sarif, csv, json")
		return
	}

	repo, err := h.db.GetRepositoryByName(ctx, name)
	if err != nil {
		h.log.Errorw("export vuln scan: db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if repo == nil || !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
		return
	}

	result, err := h.db.GetVulnScanResultByID(ctx, repo.ID, id)
	if err != nil {
		h.log.Errorw("export vuln scan: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if result == nil {
		writeError(w, http.StatusNotFound, "not_found", "vuln scan not found")
		return
	}

	h.writeVulnExport(w, format, exportFileName(format, "vuln", repo.Name, id),
		[]scanexport.VulnItem{{Repo: repo.Name, Scan: result}}, false, func() any { return vulnScanResultToMap(result) })
}

// handleExportOverview downloads the findings of every scan the overview's
// filters select (all pages) as SARIF, CSV or JSON.
func (h *VulnHandler) handleExportOverview(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r, h.evaluator) {
		return
	}

	format, ok := exportFormat(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be one of: sarif, csv, json")
		return
	}

	params, msg := vulnOverviewParams(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	// An export is the whole selection, not a page.
	params.After = nil
	params.Limit = vulnOverviewMaxLimit

	ctx := r.Context()
	items := []scanexport.VulnItem{}
	truncated := false

	for {
		page, err := h.db.ListVulnScanOverview(ctx, params)
		if err != nil {
			if errors.Is(err, database.ErrInvalidVulnScanFilter) {
				writeError(w, http.StatusBadRequest, "validation_failed", "invalid filter")
				return
			}

			h.log.Errorw("export vuln scans: db error", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		for _, row := range page.Scans {
			if len(items) >= vulnExportMaxScans {
				truncated = true
				break
			}

			scan := &row.VulnScanResult
			if row.VulnCount > 0 {
				full, err := h.db.GetVulnScanResultByID(ctx, row.RepositoryID, row.ID)
				if err != nil {
					h.log.Errorw("export vuln scans: db error reading findings", "error", err)
					writeError(w, http.StatusInternalServerError, "internal_error", "database error")

					return
				}

				if full == nil {
					continue // deleted since the page was read
				}

				scan = full
			}

			items = append(items, scanexport.VulnItem{Repo: row.RepositoryName, Scan: scan})
		}

		if truncated || page.Next == nil {
			break
		}

		params.After = page.Next
	}

	h.writeVulnExport(w, format, exportFileName(format, "vuln-scans"), items, truncated, func() any {
		out := make([]responses.VulnScanExportResponse, 0, len(items))

		for _, it := range items {
			resp := vulnScanResultToMap(it.Scan)
			if it.Scan.Vulnerabilities == nil {
				resp.Vulnerabilities = []models.Vulnerability{}
			}

			out = append(out, responses.VulnScanExportResponse{VulnScanResultResponse: resp, RepositoryName: it.Repo})
		}

		return map[string]any{"scans": out, "truncated": truncated}
	})
}

// writeVulnExport renders items in format; asJSON builds the JSON body.
func (h *VulnHandler) writeVulnExport(w http.ResponseWriter, format, filename string, items []scanexport.VulnItem, truncated bool, asJSON func() any) {
	switch format {
	case exportFormatSARIF:
		log, cut := scanexport.VulnSARIF(items)
		exportHeaders(w, format, filename, truncated || cut)
		writeExportJSON(w, log)
	case exportFormatCSV:
		exportHeaders(w, format, filename, truncated)
		writeExportCSV(w, scanexport.VulnCSVHeader, scanexport.VulnCSVRows(items))
	default:
		exportHeaders(w, format, filename, truncated)
		writeExportJSON(w, asJSON())
	}
}
