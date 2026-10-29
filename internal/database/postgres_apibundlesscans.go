// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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

// DeleteSecretScanResult deletes a secret scan result (and its owning scan job)
// by ID, scoped to repoID. Returns ErrSecretScanNotFound when nothing matched.
func (p *PostgresDB) DeleteSecretScanResult(ctx context.Context, repoID, id string) error {
	return p.execMatching(ctx, "delete secret scan result", ErrSecretScanNotFound, `
		DELETE FROM scan_jobs sj
		USING secret_results sr, artifact_refs ref
		WHERE sr.id = $2 AND sr.scan_job_id = sj.id AND sj.scan_type = 'secret'
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

// GetLicenseScanResultByID returns the license scan result with the given ID scoped to repoID, or nil.
func (p *PostgresDB) GetLicenseScanResultByID(ctx context.Context, repoID, id string) (*models.LicenseScanResult, error) {
	result, err := scanLicenseResult(p.db.QueryRowContext(ctx, `
		SELECT `+licenseResultColumns+`
		FROM license_results lr
		JOIN scan_jobs sj ON sj.id = lr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'license' AND ref.repository_id = $1 AND lr.id = $2`, repoID, id,
	))

	return orNotFound(result, err, "get license scan result by id")
}

// ListGlobalLicensePolicies lists only the global (repository_id IS NULL) license policies.
func (p *PostgresDB) ListGlobalLicensePolicies(ctx context.Context) ([]*models.LicensePolicy, error) {
	// A NULL repository filter makes "repository_id = $1" never match, leaving
	// only the "repository_id IS NULL" branch of the shared query.
	rows, err := p.q.ListLicensePolicies(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list global license policies: %w", err)
	}

	policies := make([]*models.LicensePolicy, 0, len(rows))

	for _, row := range rows {
		policies = append(policies, &models.LicensePolicy{
			ID:              row.ID,
			RepositoryID:    row.RepositoryID,
			Name:            row.Name,
			Description:     derefStr(row.Description),
			Action:          row.Action,
			AllowedLicenses: row.AllowedLicenses,
			DeniedLicenses:  row.DeniedLicenses,
			Enabled:         row.Enabled,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
		})
	}

	return policies, nil
}

// ---------------------------------------------------------------------------
// SBOM reports
// ---------------------------------------------------------------------------

// GetSBOMReportByID returns the SBOM report with the given ID scoped to repoID, or nil.
func (p *PostgresDB) GetSBOMReportByID(ctx context.Context, repoID, id string) (*models.SBOMReport, error) {
	report := &models.SBOMReport{}

	var blobSHA256 sql.NullString

	err := p.db.QueryRowContext(ctx, `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sr.sbom_format, sr.spec_version,
		       sr.component_count, b.sha256, sj.completed_at, sj.created_at, sj.updated_at
		FROM sbom_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		LEFT JOIN blobs b ON b.id = sr.blob_id
		WHERE sj.scan_type = 'sbom' AND ref.repository_id = $1 AND sr.id = $2`, repoID, id,
	).Scan(&report.ID, &report.RepositoryID, &report.ArtifactID, &report.Format, &report.SpecVersion,
		&report.ComponentCount, &blobSHA256, &report.GeneratedAt, &report.CreatedAt, &report.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get SBOM report by id: %w", err)
	}

	report.BlobSHA256 = blobSHA256.String

	return report, nil
}

// DeleteSBOMReportInRepo deletes the SBOM report with the given ID scoped to
// repoID and returns the sha256 of the blob it referenced ("" when it had none).
// Callers should afterwards remove the blob from storage if it is unreferenced
// (storage.SafeDeleteBlob). Returns ErrSBOMReportNotFound when nothing matched.
func (p *PostgresDB) DeleteSBOMReportInRepo(ctx context.Context, repoID, id string) (string, error) {
	var blobSHA256 sql.NullString

	err := p.db.QueryRowContext(ctx, `
		WITH del AS (
			DELETE FROM sbom_results sr
			USING scan_jobs sj, artifact_refs ref
			WHERE sr.id = $2 AND sj.id = sr.scan_job_id AND sj.scan_type = 'sbom'
			  AND ref.id = sj.artifact_ref_id AND ref.repository_id = $1
			RETURNING sr.blob_id
		)
		SELECT b.sha256 FROM del LEFT JOIN blobs b ON b.id = del.blob_id`, repoID, id,
	).Scan(&blobSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSBOMReportNotFound
	}

	if err != nil {
		return "", fmt.Errorf("failed to delete SBOM report: %w", err)
	}

	return blobSHA256.String, nil
}

// ---------------------------------------------------------------------------
// Artifact properties
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Release bundles
// ---------------------------------------------------------------------------

// AddBundleItems inserts all items atomically. The bundle row is locked and
// must be in draft state; otherwise ErrBundleNotDraft / ErrBundleNotFound is
// returned and nothing is inserted. Items whose (bundle_id, sha256) already
// exists are skipped. Each item's ID is filled in.
func (p *PostgresDB) AddBundleItems(ctx context.Context, bundleID string, items []*models.ReleaseBundleItem) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var state string

	err = tx.QueryRowContext(ctx, `SELECT state FROM release_bundles WHERE id = $1 FOR UPDATE`, bundleID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBundleNotFound
	}

	if err != nil {
		return fmt.Errorf("failed to lock release bundle: %w", err)
	}

	if state != string(models.BundleStateDraft) {
		return ErrBundleNotDraft
	}

	for _, item := range items {
		err := tx.QueryRowContext(ctx, `
			WITH ref AS (
				INSERT INTO artifact_refs (repository_id, artifact_path, format)
				VALUES ($2, $4, $5)
				ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format
				RETURNING id
			), ins AS (
				INSERT INTO release_bundle_items
				(bundle_id, source_repo_id, artifact_ref_id, sha256, artifact_path, format, size_bytes, adder_id, added_at)
				SELECT $1, $2, ref.id, $3, $4, $5, $6, (SELECT id FROM auth_users WHERE username = $7), $8
				FROM ref
				ON CONFLICT (bundle_id, sha256) DO NOTHING
				RETURNING id
			)
			SELECT COALESCE(
				(SELECT id FROM ins),
				(SELECT id FROM release_bundle_items WHERE bundle_id = $1 AND sha256 = $3))`,
			bundleID, item.SourceRepoID, item.SHA256, item.ArtifactPath, item.Format, item.SizeBytes, item.AddedBy, orNow(item.AddedAt),
		).Scan(&item.ID)
		if err != nil {
			return fmt.Errorf("failed to add bundle item %q: %w", item.SHA256, err)
		}

		item.BundleID = bundleID
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit bundle items: %w", err)
	}

	return nil
}

// SealReleaseBundle moves a draft bundle to sealed with a conditional UPDATE
// (WHERE state = 'draft'), so concurrent item adds/seals cannot interleave.
// It reports whether the bundle was sealed; false means it was no longer a
// draft (or does not exist), or, when sig is given, that its items changed
// since the manifest was signed. A bundle with no items is never sealed.
func (p *PostgresDB) SealReleaseBundle(
	ctx context.Context,
	bundleID, sealedBy string,
	sealedAt time.Time,
	sig *models.BundleSignature,
) (bool, error) {
	var keyID, signature, itemIDs *string

	if sig != nil {
		ids := strings.Join(sig.ItemIDs, ",")
		keyID, signature, itemIDs = &sig.KeyFingerprint, &sig.Signature, &ids
	}

	res, err := p.db.ExecContext(ctx, `
		UPDATE release_bundles
		SET state = 'sealed', sealed_at = $2, sealer_id = (SELECT id FROM auth_users WHERE username = $3),
		    gpg_key_id = $4, gpg_signature = $5, updated_at = now()
		WHERE id = $1 AND state = 'draft'
		  AND EXISTS (SELECT 1 FROM release_bundle_items WHERE bundle_id = $1)
		  AND ($6::text IS NULL OR $6::text = (
		        SELECT COALESCE(string_agg(id::text, ',' ORDER BY id::text COLLATE "C"), '')
		        FROM release_bundle_items WHERE bundle_id = $1))`,
		bundleID, sealedAt, sealedBy, keyID, signature, itemIDs)
	if err != nil {
		return false, fmt.Errorf("failed to seal release bundle: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to seal release bundle: %w", err)
	}

	return n > 0, nil
}
