// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
)

const (
	exportFormatVEX = "vex"

	// securityExportMaxFindings bounds how many findings a csv or json export holds.
	securityExportMaxFindings = 10000
	// securityExportMaxVEXArtifacts bounds how many artifacts a VEX export
	// speaks about (the inputs of each are read with several queries).
	securityExportMaxVEXArtifacts = 500
)

// securityCSVHeader is the column list of the CSV export, in securityCSVRows order.
func securityCSVHeader() []string {
	return []string{
		"type", "repository", "format", "package_name", "package_version", "artifact_id", "severity",
		"critical", "high", "medium", "low", "title", "scanner", "status", "quarantined", "vex_status", "scanned_at", "id",
	}
}

// securityExportFormat reads ?format: csv, json (default) or vex.
func securityExportFormat(r *http.Request) (string, bool) {
	switch f := strings.ToLower(r.URL.Query().Get("format")); f {
	case "":
		return exportFormatJSON, true
	case exportFormatCSV, exportFormatJSON, exportFormatVEX:
		return f, true
	}

	return "", false
}

// collectFindings reads every page of the selection, up to limit findings.
// truncated is true when more findings exist than were read.
func (h *SecurityHandler) collectFindings(ctx context.Context, params models.FindingListParams, limit int) ([]*models.Finding, bool, error) {
	params.After = nil
	params.Limit = securityFindingsMaxLimit

	var out []*models.Finding

	for {
		page, err := h.db.ListSecurityFindings(ctx, params)
		if err != nil {
			return nil, false, err //nolint:wrapcheck // the handler logs and maps it
		}

		for _, f := range page.Findings {
			if len(out) >= limit {
				return out, true, nil
			}

			out = append(out, f)
		}

		if page.Next == nil {
			return out, false, nil
		}

		params.After = page.Next
	}
}

// handleExport downloads the findings the list's filters select (every page) as
// csv, json or a CycloneDX VEX document of the vulnerability findings.
func (h *SecurityHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.findingScope(w, r)
	if !ok {
		return
	}

	format, ok := securityExportFormat(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be one of: csv, json, vex")
		return
	}

	params, msg := securityListParams(r, scope)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	limit := securityExportMaxFindings
	if format == exportFormatVEX {
		// A VEX document speaks about vulnerabilities only.
		limit = securityExportMaxVEXArtifacts
		params.Types = slices.DeleteFunc(slices.Clone(params.Types), func(t string) bool { return t != models.FindingVuln })
	}

	var (
		findings  []*models.Finding
		truncated bool
		err       error
	)

	if len(params.Types) > 0 {
		findings, truncated, err = h.collectFindings(r.Context(), params, limit)
	}

	if err != nil {
		if errors.Is(err, database.ErrInvalidFindingFilter) {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid filter")
			return
		}

		h.log.Errorw("security findings export: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	switch format {
	case exportFormatCSV:
		exportHeaders(w, format, exportFileName(format, "security-findings"), truncated)
		writeExportCSV(w, securityCSVHeader(), securityCSVRows(findings))
	case exportFormatVEX:
		h.writeVEXExport(w, r, findings, truncated)
	default:
		resp := make([]responses.SecurityFindingResponse, 0, len(findings))
		for _, f := range findings {
			resp = append(resp, securityFindingToResponse(f))
		}

		exportHeaders(w, format, exportFileName(format, "security-findings"), truncated)
		writeExportJSON(w, map[string]any{"findings": resp, "truncated": truncated})
	}
}

func securityCSVRows(findings []*models.Finding) [][]string {
	rows := make([][]string, 0, len(findings))

	for _, f := range findings {
		rows = append(rows, []string{
			f.Type, f.Repository, f.Format, f.PackageName, f.PackageVersion, f.ArtifactID, f.Severity(),
			strconv.Itoa(f.Critical), strconv.Itoa(f.High), strconv.Itoa(f.Medium), strconv.Itoa(f.Low),
			f.Title, f.Scanner, f.Status, strconv.FormatBool(f.Quarantined), f.VEXStatus, formatTime(f.ScannedAt), f.ID,
		})
	}

	return rows
}

// writeVEXExport builds one CycloneDX VEX document for the artifacts of the
// vulnerability findings. An artifact hosted in several repositories appears
// once (the first repository's, in finding order), keyed by its package URL.
func (h *SecurityHandler) writeVEXExport(w http.ResponseWriter, r *http.Request, findings []*models.Finding, truncated bool) {
	ctx := r.Context()

	type target struct{ repoID, artifactID string }

	seenTarget := map[target]bool{}
	seenPURL := map[string]bool{}
	inputs := []*sbom.Input{}

	for _, f := range findings {
		t := target{f.RepositoryID, f.ArtifactID}
		if seenTarget[t] {
			continue
		}

		seenTarget[t] = true

		metadata, err := storedArtifactMetadata(ctx, h.db, f.RepositoryID, f.ArtifactID)
		if err != nil {
			h.log.Errorw("security findings export: artifact metadata", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		in, err := h.generator.VEXInput(ctx, f.RepositoryID, f.Format, f.ArtifactID, metadata)
		if err != nil {
			h.log.Errorw("security findings export: vex input", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "database error")

			return
		}

		if in.PURL != "" {
			if seenPURL[in.PURL] {
				continue
			}

			seenPURL[in.PURL] = true
		}

		inputs = append(inputs, in)
	}

	result, err := h.generator.GenerateCycloneDXVEX(inputs, "")
	if err != nil {
		h.log.Errorw("security findings export: vex generation", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate VEX document")

		return
	}

	exportHeaders(w, exportFormatJSON, exportFileName("json", "security-findings-vex.cyclonedx"), truncated)
	w.Header().Set("Content-Type", result.ContentType)
	w.WriteHeader(http.StatusOK)

	//nolint:gosec // G705: a JSON document served as an attachment, not HTML
	if _, err := w.Write(result.Body); err != nil {
		h.log.Warnw("security findings export: failed to write response", "error", err)
	}
}
