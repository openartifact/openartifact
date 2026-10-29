// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// SecretScanStore owns secret scan results.
type SecretScanStore struct{ *core }

// secretScanStatusToJob maps SecretScanResult.ScanStatus's "clean"/"flagged"
// values (set from finding_count at scan time) onto scan_jobs' CHECK-constrained
// status vocabulary (pending/running/completed/failed). The clean/flagged
// distinction is recovered on read from secret_results.finding_count instead
// of being stored redundantly (see secretScanStatusFromJob).
func secretScanStatusToJob(status string) string {
	if status == "clean" || status == "flagged" {
		return "completed"
	}

	return status
}

func secretScanStatusFromJob(jobStatus string, findingCount int) string {
	if jobStatus == "completed" {
		if findingCount > 0 {
			return "flagged"
		}

		return "clean"
	}

	return jobStatus
}

// CreateSecretScanResult creates a new secret scan result (UPSERT).
func (p *SecretScanStore) CreateSecretScanResult(ctx context.Context, result *models.SecretScanResult) error {
	findingsJSON, err := json.Marshal(result.Findings)
	if err != nil {
		return fmt.Errorf("failed to marshal findings: %w", err)
	}

	var scannedAt time.Time
	if result.ScannedAt != nil {
		scannedAt = *result.ScannedAt
	}

	err = p.db.QueryRowContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE((SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message, completed_at)
			SELECT ref.id, 'secret', '', $3, NULLIF($4, ''), $5 FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message,
				completed_at = EXCLUDED.completed_at, updated_at = now()
			RETURNING id
		)
		INSERT INTO secret_results (scan_job_id, findings, finding_count, quarantined)
		SELECT job.id, $6, $7, $8 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			findings = EXCLUDED.findings, finding_count = EXCLUDED.finding_count, quarantined = EXCLUDED.quarantined
		RETURNING id`,
		result.RepositoryID, result.ArtifactID, secretScanStatusToJob(result.ScanStatus), result.ScanError, scannedAt,
		findingsJSON, result.FindingCount, result.Quarantined,
	).Scan(&result.ID)
	if err != nil {
		return fmt.Errorf("failed to create secret scan result: %w", err)
	}

	return nil
}

// GetSecretScanResult gets a secret scan result by repo and artifact ID.
func (p *SecretScanStore) GetSecretScanResult(ctx context.Context, repoID, artifactID string) (*models.SecretScanResult, error) {
	result := &models.SecretScanResult{}

	var (
		scannedAt   sql.NullTime
		errMsg      sql.NullString
		findingsRaw []byte
		jobStatus   string
	)

	err := p.db.QueryRowContext(ctx, `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sj.status, sr.findings, sr.finding_count, sr.quarantined,
		       sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
		FROM secret_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'secret' AND ref.repository_id = $1 AND ref.artifact_path = $2`,
		repoID, artifactID,
	).Scan(&result.ID, &result.RepositoryID, &result.ArtifactID, &jobStatus, &findingsRaw, &result.FindingCount, &result.Quarantined,
		&errMsg, &scannedAt, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get secret scan result: %w", err)
	}

	result.ScanStatus = secretScanStatusFromJob(jobStatus, result.FindingCount)
	result.ScanError = errMsg.String

	if scannedAt.Valid {
		result.ScannedAt = &scannedAt.Time
	}

	if len(findingsRaw) > 0 {
		if err := json.Unmarshal(findingsRaw, &result.Findings); err != nil {
			return nil, fmt.Errorf("failed to unmarshal findings: %w", err)
		}
	}

	return result, nil
}

// ListSecretScanResults lists all secret scan results for a repository.
func (p *SecretScanStore) ListSecretScanResults(ctx context.Context, repoID string, flaggedOnly bool) ([]*models.SecretScanResult, error) {
	query := `
		SELECT sr.id, ref.repository_id, ref.artifact_path, sj.status, sr.findings, sr.finding_count, sr.quarantined,
		       sj.error_message, sj.completed_at, sj.created_at, sj.updated_at
		FROM secret_results sr
		JOIN scan_jobs sj ON sj.id = sr.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id
		WHERE sj.scan_type = 'secret' AND ref.repository_id = $1`
	if flaggedOnly {
		query += ` AND sr.quarantined = true`
	}

	query += ` ORDER BY sj.created_at DESC`

	rows, err := p.db.QueryContext(ctx, query, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list secret scan results: %w", err)
	}
	defer rows.Close()

	results := make([]*models.SecretScanResult, 0)

	for rows.Next() {
		result := &models.SecretScanResult{}

		var (
			scannedAt   sql.NullTime
			errMsg      sql.NullString
			findingsRaw []byte
			jobStatus   string
		)

		if err := rows.Scan(
			&result.ID,
			&result.RepositoryID,
			&result.ArtifactID,
			&jobStatus,
			&findingsRaw,
			&result.FindingCount,
			&result.Quarantined,
			&errMsg,
			&scannedAt,
			&result.CreatedAt,
			&result.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan secret scan result: %w", err)
		}

		result.ScanStatus = secretScanStatusFromJob(jobStatus, result.FindingCount)
		result.ScanError = errMsg.String

		if scannedAt.Valid {
			result.ScannedAt = &scannedAt.Time
		}

		if len(findingsRaw) > 0 {
			if err := json.Unmarshal(findingsRaw, &result.Findings); err != nil {
				return nil, fmt.Errorf("failed to unmarshal findings: %w", err)
			}
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

// DeleteSecretScanResult deletes a secret scan result (and its owning scan job)
// by ID, scoped to repoID. Returns ErrSecretScanNotFound when nothing matched.
func (p *SecretScanStore) DeleteSecretScanResult(ctx context.Context, repoID, id string) error {
	return p.execMatching(ctx, "delete secret scan result", ErrSecretScanNotFound, `
		DELETE FROM scan_jobs sj
		USING secret_results sr, artifact_refs ref
		WHERE sr.id = $2 AND sr.scan_job_id = sj.id AND sj.scan_type = 'secret'
		  AND ref.id = sj.artifact_ref_id AND ref.repository_id = $1`, repoID, id)
}
