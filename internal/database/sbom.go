// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
)

// SBOMStore owns generated SBOM reports.
type SBOMStore struct{ *core }

// CreateSBOMReport creates or updates an SBOM report (upsert). The caller
// (internal/api/sbom.go) is expected to have already written the SBOM bytes
// into the blob store and set report.BlobSHA256/BlobSizeBytes; this upserts
// the corresponding blobs row and stores the resulting blob_id alongside
// the report metadata. A zero-value BlobSHA256 stores a NULL blob_id.
func (p *SBOMStore) CreateSBOMReport(ctx context.Context, report *models.SBOMReport) error {
	var blobID *string

	if report.BlobSHA256 != "" {
		id, err := p.upsertBlobWithMediaType(ctx, report.BlobSHA256, report.BlobSHA256, report.ContentType, report.BlobSizeBytes)
		if err != nil {
			return fmt.Errorf("failed to upsert blob for sbom report: %w", err)
		}

		blobID = &id
	}

	err := p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, $3)
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET format = EXCLUDED.format
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, completed_at)
			SELECT ref.id, 'sbom', '', 'completed', $4 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id, created_at, updated_at
		), ins AS (
			INSERT INTO sbom_results (scan_job_id, sbom_format, spec_version, blob_id, component_count)
			SELECT job.id, $3, $5, $7, $6 FROM job
			ON CONFLICT (scan_job_id, sbom_format, spec_version) DO UPDATE SET
				component_count = EXCLUDED.component_count, blob_id = EXCLUDED.blob_id
			RETURNING id, scan_job_id
		)
		SELECT ins.id, job.created_at, job.updated_at FROM ins JOIN job ON job.id = ins.scan_job_id`,
		report.RepositoryID, report.ArtifactID, report.Format, report.GeneratedAt, report.SpecVersion, report.ComponentCount, blobID,
	).Scan(&report.ID, &report.CreatedAt, &report.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create SBOM report: %w", err)
	}

	return nil
}

// ListSBOMReports lists all SBOM reports for a repository.
func (p *SBOMStore) ListSBOMReports(ctx context.Context, repoID string) ([]*models.SBOMReport, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sr.sbom_format, sr.spec_version,
		       sr.component_count, b.sha256, sj.completed_at, sj.created_at, sj.updated_at
		FROM sbom_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		LEFT JOIN blobs b ON b.id = sr.blob_id
		WHERE sj.scan_type = 'sbom' AND ref.repository_id = $1
		ORDER BY sj.created_at DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list SBOM reports: %w", err)
	}
	defer rows.Close()

	var reports []*models.SBOMReport

	for rows.Next() {
		report := &models.SBOMReport{}

		var blobSHA256 sql.NullString

		if err := rows.Scan(&report.ID, &report.RepositoryID, &report.ArtifactID, &report.Format, &report.SpecVersion,
			&report.ComponentCount, &blobSHA256, &report.GeneratedAt, &report.CreatedAt, &report.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan SBOM report: %w", err)
		}

		report.BlobSHA256 = blobSHA256.String

		reports = append(reports, report)
	}

	return reports, rows.Err()
}

// GetSBOMReportByID returns the SBOM report with the given ID scoped to repoID, or nil.
func (p *SBOMStore) GetSBOMReportByID(ctx context.Context, repoID, id string) (*models.SBOMReport, error) {
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
func (p *SBOMStore) DeleteSBOMReportInRepo(ctx context.Context, repoID, id string) (string, error) {
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
