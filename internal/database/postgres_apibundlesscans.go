// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"

	"github.com/openartifact/openartifact/internal/database/models"
)

var (
	// ErrVulnScanNotFound is returned when a vuln scan result does not exist in the repository.
	ErrVulnScanNotFound = errors.New("vuln scan result not found")

	// ErrSecretScanNotFound is returned when a secret scan result does not exist in the repository.
	ErrSecretScanNotFound = errors.New("secret scan result not found")

	// ErrBundleNotDraft is returned when a bundle mutation requires the draft state.
	ErrBundleNotDraft = errors.New("release bundle is not in draft state")

	// ErrBundleNotFound is returned when a release bundle does not exist.
	ErrBundleNotFound = errors.New("release bundle not found")
)

// ---------------------------------------------------------------------------
// Vulnerability scans
// ---------------------------------------------------------------------------

const vulnScanByIDSelect = `
	SELECT ` + vulnResultColumns + `
	FROM vuln_results vr
	JOIN scan_jobs sj ON sj.id = vr.scan_job_id
	JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
	WHERE sj.scan_type = 'vuln' AND ref.repository_id = $1 AND vr.id = $2`

// GetVulnScanResultByID returns the vuln scan result with the given ID scoped to repoID, or nil.
func (p *PostgresDB) GetVulnScanResultByID(ctx context.Context, repoID, id string) (*models.VulnScanResult, error) {
	result, err := scanVulnResult(p.db.QueryRowContext(ctx, vulnScanByIDSelect, repoID, id))

	return orNotFound(result, err, "get vuln scan result by id")
}

// DeleteVulnScanResult deletes a vuln scan result (and its owning scan job)
// by ID, scoped to repoID. Returns ErrVulnScanNotFound when nothing matched.
func (p *PostgresDB) DeleteVulnScanResult(ctx context.Context, repoID, id string) error {
	return p.execMatching(ctx, "delete vuln scan result", ErrVulnScanNotFound, `
		DELETE FROM scan_jobs sj
		USING vuln_results vr, artifact_refs ref
		WHERE vr.id = $2 AND vr.scan_job_id = sj.id AND sj.scan_type = 'vuln'
		  AND ref.id = sj.artifact_ref_id AND ref.repository_id = $1`, repoID, id)
}

// SetVulnScanQuarantine sets the quarantined flag of exactly one vuln scan
// result (by ID, scoped to repoID). Returns ErrVulnScanNotFound when nothing matched.
func (p *PostgresDB) SetVulnScanQuarantine(ctx context.Context, repoID, id string, quarantined bool) error {
	return p.execMatching(ctx, "set vuln scan quarantine", ErrVulnScanNotFound, `
		UPDATE vuln_results vr SET quarantined = $3
		FROM scan_jobs sj, artifact_refs ref
		WHERE vr.id = $2 AND sj.id = vr.scan_job_id AND sj.scan_type = 'vuln'
		  AND ref.id = sj.artifact_ref_id AND ref.repository_id = $1`, repoID, id, quarantined)
}

// ---------------------------------------------------------------------------
// License scans and policies
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SBOM reports
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Artifact properties
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Release bundles
// ---------------------------------------------------------------------------
