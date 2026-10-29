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

	"github.com/openartifact/openartifact/internal/database/models"
)

// AIAnalysisStore owns AI analysis results.
type AIAnalysisStore struct{ *core }

// aiStatusToJob/FromJob map AIAnalysis.Status's "processing" onto scan_jobs'
// "running" (the CHECK-constrained vocabulary), and back.
func aiStatusToJob(status string) string {
	if status == "processing" {
		return "running"
	}

	return status
}

func aiStatusFromJob(status string) string {
	if status == "running" {
		return "processing"
	}

	return status
}

// UpsertAIAnalysis inserts or updates an AI analysis record.
//
// AI analyses are stored across scan_jobs + artifact_refs + ai_results;
// ArtifactName/ArtifactVersion live on ai_results (artifact_refs only tracks
// repository_id/artifact_path/format). analysis.Status's "processing" value is
// mapped onto scan_jobs' CHECK-constrained "running" (see aiStatusToJob/FromJob).
//
// Only a "completed" analysis overwrites the result columns (summary, tags,
// license, security notes, model, token counts). A "processing", "pending" or
// "failed" upsert keeps what the last completed run stored, so a re-trigger or
// a failed re-analysis never wipes a good result; the status and error live on
// scan_jobs and always follow the latest upsert.
func (p *AIAnalysisStore) UpsertAIAnalysis(ctx context.Context, analysis *models.AIAnalysis) error {
	tagsJSON, err := json.Marshal(analysis.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}

	_, err = p.db.ExecContext(ctx, `
		WITH ref AS (
			INSERT INTO artifact_refs (repository_id, artifact_path, format)
			VALUES ($1, $2, COALESCE($3, (SELECT format FROM repositories WHERE id = $1), 'unknown'))
			ON CONFLICT (repository_id, artifact_path) DO UPDATE SET artifact_path = EXCLUDED.artifact_path
			RETURNING id
		), job AS (
			INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status, error_message)
			SELECT ref.id, 'ai', '', $4, NULLIF($5, '') FROM ref
			ON CONFLICT (artifact_ref_id, scan_type, scanner) DO UPDATE SET
				status = EXCLUDED.status, error_message = EXCLUDED.error_message, updated_at = now()
			RETURNING id
		)
		INSERT INTO ai_results (scan_job_id, summary, tags, license_status, license_name, security_notes, quarantine_recommended,
			model_used, prompt_tokens, completion_tokens, artifact_name, artifact_version)
		SELECT job.id, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16 FROM job
		ON CONFLICT (scan_job_id) DO UPDATE SET
			summary = CASE WHEN $4::text = 'completed' THEN EXCLUDED.summary ELSE ai_results.summary END,
			tags = CASE WHEN $4::text = 'completed' THEN EXCLUDED.tags ELSE ai_results.tags END,
			license_status = CASE WHEN $4::text = 'completed' THEN EXCLUDED.license_status ELSE ai_results.license_status END,
			license_name = CASE WHEN $4::text = 'completed' THEN EXCLUDED.license_name ELSE ai_results.license_name END,
			security_notes = CASE WHEN $4::text = 'completed' THEN EXCLUDED.security_notes ELSE ai_results.security_notes END,
			quarantine_recommended = CASE WHEN $4::text = 'completed' THEN EXCLUDED.quarantine_recommended ELSE
				ai_results.quarantine_recommended END,
			model_used = CASE WHEN $4::text = 'completed' THEN EXCLUDED.model_used ELSE ai_results.model_used END,
			prompt_tokens = CASE WHEN $4::text = 'completed' THEN EXCLUDED.prompt_tokens ELSE ai_results.prompt_tokens END,
			completion_tokens = CASE WHEN $4::text = 'completed' THEN EXCLUDED.completion_tokens ELSE ai_results.completion_tokens END,
			artifact_name = EXCLUDED.artifact_name, artifact_version = EXCLUDED.artifact_version`,
		analysis.RepositoryID, analysis.ArtifactID, strPtr(analysis.ArtifactFormat), aiStatusToJob(analysis.Status), analysis.ErrorMessage,
		analysis.Summary, tagsJSON, analysis.LicenseStatus, analysis.LicenseName, analysis.SecurityNotes,
		analysis.QuarantineRecommended, analysis.ModelUsed, analysis.PromptTokens, analysis.CompletionTokens,
		analysis.ArtifactName, analysis.ArtifactVersion,
	)

	return err
}

const aiAnalysisSelect = `SELECT air.id, ref.repository_id, ref.artifact_path, ref.format,
	       air.summary, air.tags, air.license_status, air.license_name, air.security_notes,
	       air.quarantine_recommended, sj.status, sj.error_message, air.model_used,
	       air.prompt_tokens, air.completion_tokens, sj.created_at, sj.updated_at,
	       air.artifact_name, air.artifact_version
	FROM ai_results air
	JOIN scan_jobs sj ON sj.id = air.scan_job_id
	JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id`

// GetAIAnalysis retrieves an AI analysis by repo and artifact ID.
func (p *AIAnalysisStore) GetAIAnalysis(ctx context.Context, repoID, artifactID string) (*models.AIAnalysis, error) {
	row := p.db.QueryRowContext(ctx,
		aiAnalysisSelect+` WHERE sj.scan_type = 'ai' AND ref.repository_id = $1 AND ref.artifact_path = $2`,
		repoID, artifactID)

	return scanAIAnalysis(row)
}

// ListAIAnalyses lists AI analyses for a repository with optional status and
// REVIEW_REQUIRED license filters.
func (p *AIAnalysisStore) ListAIAnalyses(
	ctx context.Context,
	repoID string,
	status string,
	licenseReviewOnly bool,
	limit, offset int,
) ([]*models.AIAnalysis, int, error) {
	args := []any{repoID}
	where := "WHERE sj.scan_type = 'ai' AND ref.repository_id = $1"

	if status != "" {
		args = append(args, aiStatusToJob(status))
		where += fmt.Sprintf(" AND sj.status = $%d", len(args))
	}

	if licenseReviewOnly {
		where += " AND air.license_status = 'REVIEW_REQUIRED'"
	}

	var total int

	countQuery := `SELECT COUNT(*) FROM ai_results air
		JOIN scan_jobs sj ON sj.id = air.scan_job_id
		JOIN artifact_refs ref ON ref.id = sj.artifact_ref_id ` + where
	if err := p.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)

	//nolint:gosec // G202: where clause is built from controlled inputs only ($1 placeholder + static AND status = $N)
	query := aiAnalysisSelect + ` ` + where + fmt.Sprintf(` ORDER BY sj.updated_at DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}

	defer rows.Close()

	var results []*models.AIAnalysis

	for rows.Next() {
		a, err := scanAIAnalysisRow(rows)
		if err != nil {
			return nil, 0, err
		}

		results = append(results, a)
	}

	return results, total, rows.Err()
}

type aiAnalysisScanner interface {
	Scan(dest ...any) error
}

func scanAIAnalysis(s aiAnalysisScanner) (*models.AIAnalysis, error) {
	return scanAIAnalysisRow(s)
}

// scanAIAnalysisRow scans an aiAnalysisSelect row.
func scanAIAnalysisRow(s aiAnalysisScanner) (*models.AIAnalysis, error) {
	var a models.AIAnalysis

	var tagsJSON []byte

	var errMsg sql.NullString

	var jobStatus string

	err := s.Scan(
		&a.ID, &a.RepositoryID, &a.ArtifactID, &a.ArtifactFormat,
		&a.Summary, &tagsJSON, &a.LicenseStatus, &a.LicenseName,
		&a.SecurityNotes, &a.QuarantineRecommended, &jobStatus,
		&errMsg, &a.ModelUsed,
		&a.PromptTokens, &a.CompletionTokens,
		&a.CreatedAt, &a.UpdatedAt,
		&a.ArtifactName, &a.ArtifactVersion,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
		}

		return nil, err
	}

	a.Status = aiStatusFromJob(jobStatus)
	a.ErrorMessage = errMsg.String

	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &a.Tags)
	}

	return &a, nil
}
