// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
)

// ErrProjectQuotaExceeded is returned when moving a repository would push the
// target project past its storage ceiling.
var ErrProjectQuotaExceeded = errors.New("project storage quota would be exceeded")

// repoUsageSumsSQL sums the repository counters of project $1 (trashed
// repositories included: they keep their usage until purged), the value a
// project counter must equal.
const repoUsageSumsSQL = `
SELECT COALESCE(SUM(sq.current_size_bytes), 0)::bigint, COALESCE(SUM(sq.current_artifact_count), 0)::bigint
  FROM repositories r
  JOIN storage_quotas sq ON sq.repository_id = r.id
 WHERE r.project_id = $1`

// GetProjectQuota returns the project's storage ceiling; a project that never
// had one reads as unlimited (zero limits).
func (p *PostgresDB) GetProjectQuota(ctx context.Context, projectID string) (*models.ProjectQuota, error) {
	q := &models.ProjectQuota{ProjectID: projectID, WarnThresholdPercent: 80}

	err := p.db.QueryRowContext(ctx,
		`SELECT max_size_bytes, max_artifact_count, warn_threshold_percent, current_size_bytes, current_artifact_count, updated_at
		   FROM project_quotas WHERE project_id = $1`, projectID,
	).Scan(&q.MaxSizeBytes, &q.MaxArtifactCount, &q.WarnThresholdPercent, &q.CurrentSizeBytes, &q.CurrentArtifactCount, &q.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get project quota: %w", err)
	}

	return q, nil
}

// SetProjectQuota stores the project's storage ceiling (0 = unlimited) and
// returns it. Enabling a limit initialises the project counters from the
// repository counters in the same transaction; disabling it zeroes them (they
// are only maintained while a limit is set).
func (p *PostgresDB) SetProjectQuota(ctx context.Context, projectID string, maxSizeBytes, maxArtifactCount int64, warnThresholdPercent int) (*models.ProjectQuota, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO project_quotas (project_id) VALUES ($1) ON CONFLICT (project_id) DO NOTHING`, projectID); err != nil {
		if IsForeignKeyViolation(err) {
			return nil, ErrProjectNotFound
		}

		return nil, fmt.Errorf("failed to create project quota: %w", err)
	}

	var wasLimited bool
	if err := tx.QueryRowContext(ctx,
		`SELECT max_size_bytes > 0 OR max_artifact_count > 0 FROM project_quotas WHERE project_id = $1 FOR UPDATE`, projectID,
	).Scan(&wasLimited); err != nil {
		return nil, fmt.Errorf("failed to lock project quota: %w", err)
	}

	limited := maxSizeBytes > 0 || maxArtifactCount > 0

	var curSize, curCount int64

	switch {
	case limited && !wasLimited:
		if err := tx.QueryRowContext(ctx, repoUsageSumsSQL, projectID).Scan(&curSize, &curCount); err != nil {
			return nil, fmt.Errorf("failed to sum repository usage: %w", err)
		}

		_, err = tx.ExecContext(ctx,
			`UPDATE project_quotas SET max_size_bytes = $2, max_artifact_count = $3, warn_threshold_percent = $4,
			        current_size_bytes = $5, current_artifact_count = $6 WHERE project_id = $1`,
			projectID, maxSizeBytes, maxArtifactCount, warnThresholdPercent, curSize, curCount)
	case limited:
		_, err = tx.ExecContext(ctx,
			`UPDATE project_quotas SET max_size_bytes = $2, max_artifact_count = $3, warn_threshold_percent = $4 WHERE project_id = $1`,
			projectID, maxSizeBytes, maxArtifactCount, warnThresholdPercent)
	default:
		_, err = tx.ExecContext(ctx,
			`UPDATE project_quotas SET max_size_bytes = 0, max_artifact_count = 0, warn_threshold_percent = $2,
			        current_size_bytes = 0, current_artifact_count = 0 WHERE project_id = $1`,
			projectID, warnThresholdPercent)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to update project quota: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit project quota: %w", err)
	}

	return p.GetProjectQuota(ctx, projectID)
}

// GetProjectUsage reports a project's usage: the sum of its repositories'
// counters (always available, whether or not a ceiling is set), the ceiling,
// and one line per live repository, largest first.
func (p *PostgresDB) GetProjectUsage(ctx context.Context, projectID string) (*models.ProjectUsage, error) {
	quota, err := p.GetProjectQuota(ctx, projectID)
	if err != nil {
		return nil, err
	}

	usage := &models.ProjectUsage{Quota: *quota, Repositories: []models.ProjectRepoUsage{}}

	if err := p.db.QueryRowContext(ctx, repoUsageSumsSQL, projectID).Scan(&usage.SizeBytes, &usage.ArtifactCount); err != nil {
		return nil, fmt.Errorf("failed to sum project usage: %w", err)
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT r.id, r.name, r.format, r.repo_type,
		        COALESCE(sq.current_size_bytes, 0), COALESCE(sq.current_artifact_count, 0),
		        COALESCE(sq.max_size_bytes, 0), COALESCE(sq.max_artifact_count, 0), COALESCE(sq.warn_threshold_percent, 80)
		   FROM repositories r
		   LEFT JOIN storage_quotas sq ON sq.repository_id = r.id
		  WHERE r.project_id = $1 AND r.deleted_at IS NULL
		  ORDER BY COALESCE(sq.current_size_bytes, 0) DESC, r.name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project repository usage: %w", err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var u models.ProjectRepoUsage
		if err := rows.Scan(&u.RepositoryID, &u.Name, &u.Format, &u.RepoType, &u.SizeBytes, &u.ArtifactCount,
			&u.MaxSizeBytes, &u.MaxArtifactCount, &u.WarnThresholdPercent); err != nil {
			return nil, fmt.Errorf("failed to scan project repository usage: %w", err)
		}

		usage.Repositories = append(usage.Repositories, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate project repository usage: %w", err)
	}

	return usage, nil
}

// moveRepositoryUsage moves the repository's usage counters from its current
// project's ceiling to the target's inside the transfer transaction, and fails
// with ErrProjectQuotaExceeded when the target's ceiling cannot take them.
// Lock order as for reservations: both project quota rows (id order), then the
// repository counters.
func moveRepositoryUsage(ctx context.Context, tx *sql.Tx, repoID, toProjectID string) error {
	var fromProjectID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM repositories WHERE id = $1 AND deleted_at IS NULL`, repoID).Scan(&fromProjectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRepositoryNotFound
		}

		return fmt.Errorf("failed to read repository project: %w", err)
	}

	if fromProjectID.Valid && fromProjectID.String == toProjectID {
		return nil
	}

	ids := []string{toProjectID}
	if fromProjectID.Valid {
		ids = append(ids, fromProjectID.String)
	}

	limited, err := lockLimitedProjects(ctx, tx, ids)
	if err != nil {
		return err
	}

	if len(limited) == 0 {
		return nil
	}

	var size, count int64

	err = tx.QueryRowContext(ctx,
		`SELECT current_size_bytes, current_artifact_count FROM storage_quotas WHERE repository_id = $1 FOR UPDATE`, repoID,
	).Scan(&size, &count)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to lock repository usage: %w", err)
	}

	if to, ok := limited[toProjectID]; ok {
		if (to.MaxSizeBytes > 0 && to.CurrentSizeBytes+size > to.MaxSizeBytes) ||
			(to.MaxArtifactCount > 0 && to.CurrentArtifactCount+count > to.MaxArtifactCount) {
			return ErrProjectQuotaExceeded
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE project_quotas SET current_size_bytes = current_size_bytes + $2, current_artifact_count = current_artifact_count + $3 WHERE project_id = $1`,
			toProjectID, size, count); err != nil {
			return fmt.Errorf("failed to add usage to target project: %w", err)
		}
	}

	if _, ok := limited[fromProjectID.String]; ok && fromProjectID.Valid {
		if _, err := tx.ExecContext(ctx,
			`UPDATE project_quotas SET current_size_bytes = GREATEST(0, current_size_bytes - $2),
			        current_artifact_count = GREATEST(0, current_artifact_count - $3) WHERE project_id = $1`,
			fromProjectID.String, size, count); err != nil {
			return fmt.Errorf("failed to remove usage from source project: %w", err)
		}
	}

	return nil
}

// lockLimitedProjects locks the quota rows of the given projects in id order
// (the lock order shared by every writer) and returns those with a limit set.
func lockLimitedProjects(ctx context.Context, tx *sql.Tx, ids []string) (map[string]*models.ProjectQuota, error) {
	slices.Sort(ids)

	limited := make(map[string]*models.ProjectQuota, len(ids))

	for _, id := range ids {
		q := &models.ProjectQuota{ProjectID: id}

		err := tx.QueryRowContext(ctx,
			`SELECT max_size_bytes, max_artifact_count, current_size_bytes, current_artifact_count
			   FROM project_quotas WHERE project_id = $1 FOR UPDATE`, id,
		).Scan(&q.MaxSizeBytes, &q.MaxArtifactCount, &q.CurrentSizeBytes, &q.CurrentArtifactCount)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return nil, fmt.Errorf("failed to lock project quota: %w", err)
		}

		if q.Limited() {
			limited[id] = q
		}
	}

	return limited, nil
}
