// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/vuln"
)

// maxImportBytes bounds the body of an imported scan result.
const maxImportBytes = 32 << 20

// externalScannerPrefix starts the scanner name of an imported scan; what
// follows is the importer-chosen label (default: the format).
const externalScannerPrefix = "external:"

var importLabelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// importScanRequest is the body of POST .../vuln/scans/import.
type importScanRequest struct {
	// Format is sarif, osv-scanner or oa.
	Format string `json:"format"`
	// Digest is the manifest digest of the scanned image.
	Digest string `json:"digest"`
	// Scanner optionally names the tool that produced the result ("clair").
	Scanner string `json:"scanner"`
	// Result is the scan result in the declared format, embedded as JSON.
	Result json.RawMessage `json:"result"`
}

// handleImportScan serves POST /repositories/{name}/vuln/scans/import: it stores
// the result of a scanner that ran elsewhere (Konflux, a CI job) as a completed
// scan of the image, so the gate, quarantine and webhooks treat it like a scan
// of this server. It needs write access to the repository.
func (h *VulnHandler) handleImportScan(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.permittedRepo(w, r, "import vuln scan", "write", false)
	if !ok {
		return
	}

	req, ok := readImportRequest(w, r)
	if !ok {
		return
	}

	if repo.Format != "docker" {
		writeError(w, http.StatusBadRequest, "validation_failed", "scan results can only be imported into a docker repository")
		return
	}

	vulns, err := vuln.ParseExternal(req.Format, req.Result)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_scan_result", importErrorMessage(err))
		return
	}

	label := req.Scanner
	if label == "" {
		label = req.Format
	}

	now := time.Now()
	scan := &models.VulnScanResult{
		RepositoryID:    repo.ID,
		ArtifactID:      req.Digest,
		PackageName:     req.Digest,
		Ecosystem:       vuln.ImageEcosystem,
		Scanner:         externalScannerPrefix + label,
		ScanStatus:      "completed",
		Vulnerabilities: vulns,
		VulnCount:       len(vulns),
		FailedScanners:  []string{},
		ScannedAt:       &now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	scan.CriticalCount, scan.HighCount, scan.MediumCount, scan.LowCount = vuln.CountSeverities(vulns)

	if err := h.db.CreateVulnScanResult(r.Context(), scan); err != nil {
		h.dbError(w, "import vuln scan: storing the scan", err)
		return
	}

	h.finishImported(r.Context(), scan)

	resp, err := h.scanResponse(r.Context(), scan)
	if err != nil {
		h.dbError(w, "import vuln scan: exploit signals", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// readImportRequest reads and validates the body: 413 over the cap, 400 for
// malformed JSON, 422 for an unknown format, a bad digest or label.
func readImportRequest(w http.ResponseWriter, r *http.Request) (importScanRequest, bool) {
	var req importScanRequest

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBytes))
	if err == nil {
		err = json.Unmarshal(body, &req)
	}

	if err != nil {
		writeBadBody(w, err, "bad_request", "invalid JSON body")
		return req, false
	}

	switch {
	case !slices.Contains(vuln.ExternalFormats(), req.Format):
		writeError(w, http.StatusUnprocessableEntity, "invalid_scan_result",
			"format must be one of: "+strings.Join(vuln.ExternalFormats(), ", "))
	case !imageDigestRe.MatchString(req.Digest):
		writeError(w, http.StatusUnprocessableEntity, "invalid_scan_result", "digest must be a manifest digest (sha256:<hex>)")
	case req.Scanner != "" && !importLabelRe.MatchString(req.Scanner):
		writeError(w, http.StatusUnprocessableEntity, "invalid_scan_result", "scanner must be 1-32 characters of a-z, 0-9, '.', '_' or '-'")
	case len(req.Result) == 0:
		writeError(w, http.StatusUnprocessableEntity, "invalid_scan_result", "result is required")
	default:
		return req, true
	}

	return req, false
}

// importErrorMessage is the client-safe text for a rejected scan result.
func importErrorMessage(err error) string {
	if errors.Is(err, vuln.ErrExternalInvalid) {
		return err.Error()
	}

	return "the scan result could not be read"
}

// finishImported runs what the end of a scan runs: the exploit aggregates, the
// events and the scan hook.
func (h *VulnHandler) finishImported(ctx context.Context, scan *models.VulnScanResult) {
	ctx = context.WithoutCancel(ctx)

	if aggs, err := h.exploit.Aggregates(ctx, scan.Vulnerabilities); err != nil {
		h.log.Errorw("import vuln scan: reading exploit signals", "error", err)
	} else if len(aggs) == 1 {
		scan.KEVCount, scan.MaxEPSS = aggs[0].KEVCount, aggs[0].MaxEPSS

		if err := h.db.UpdateVulnScanResult(ctx, scan); err != nil {
			h.log.Errorw("import vuln scan: storing the exploit aggregates", "error", err)
		}
	}

	h.announceScan(ctx, scan)

	if h.onScanned != nil {
		h.onScanned(scan.RepositoryID, scan.ArtifactID)
	}
}
