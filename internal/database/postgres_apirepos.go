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

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	// ON DELETE RESTRICT reports restrict_violation instead of
	// foreign_key_violation.
	pgRestrictViolation = "23001"
)

// IsUniqueViolation reports whether err is a PostgreSQL unique_violation.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// IsForeignKeyViolation reports whether err is a PostgreSQL foreign_key_violation
// or restrict_violation: a referenced row cannot be removed or a referencing
// row points nowhere.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && (pgErr.Code == pgForeignKeyViolation || pgErr.Code == pgRestrictViolation)
}

// CreateRepositoryWithMembers inserts a repository and (for group repos) its
// ordered member list in a single transaction, so a failing member insert
// cannot leave a half-created group behind.
func (p *PostgresDB) CreateRepositoryWithMembers(ctx context.Context, repo *models.Repository, memberIDs []string) error {
	return p.createRepository(ctx, repo, memberIDs, 0)
}

// CreateProjectRepository is CreateRepositoryWithMembers under the project's
// repository cap: with the project row locked it refuses (ErrProjectRepoLimit)
// when the project already holds maxRepositories live repositories, so
// concurrent creations cannot overshoot the cap.
func (p *PostgresDB) CreateProjectRepository(ctx context.Context, repo *models.Repository, memberIDs []string, maxRepositories int) error {
	return p.createRepository(ctx, repo, memberIDs, maxRepositories)
}

// MoveRepositoryToProject reassigns a repository to another project, under the
// target's repository cap when maxRepositories > 0 (ErrProjectRepoLimit). The
// repository's storage usage moves between the projects' ceilings in the same
// transaction and must fit the target's (ErrProjectQuotaExceeded).
func (p *PostgresDB) MoveRepositoryToProject(ctx context.Context, repoID, toProjectID string, maxRepositories int) error {
	// Cached repository lookups carry the project; they must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockProjectCap(ctx, tx, toProjectID, maxRepositories); err != nil {
		return err
	}

	// The repository's usage follows it to the target project's ceiling.
	if err := moveRepositoryUsage(ctx, tx, repoID, toProjectID); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, `UPDATE repositories SET project_id = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, repoID, toProjectID)
	if err != nil {
		return fmt.Errorf("failed to move repository: %w", err)
	}

	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrRepositoryNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit repository move: %w", err)
	}

	return nil
}

// lockProjectCap locks the project row and fails with ErrProjectRepoLimit when
// it already holds maxRepositories live repositories (0 = no check).
func lockProjectCap(ctx context.Context, tx *sql.Tx, projectID string, maxRepositories int) error {
	if maxRepositories <= 0 {
		return nil
	}

	// Lock first, count second: a count in the same statement would use the
	// snapshot taken before the lock was granted and miss a concurrent insert.
	var locked string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE id = $1 FOR UPDATE`, projectID).Scan(&locked); err != nil {
		return fmt.Errorf("failed to lock project: %w", err)
	}

	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM repositories WHERE project_id = $1 AND deleted_at IS NULL`, projectID).Scan(&count); err != nil {
		return fmt.Errorf("failed to count project repositories: %w", err)
	}

	if count >= maxRepositories {
		return ErrProjectRepoLimit
	}

	return nil
}

func (p *PostgresDB) createRepository(ctx context.Context, repo *models.Repository, memberIDs []string, maxRepositories int) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if repo.ProjectID != nil {
		if err := lockProjectCap(ctx, tx, *repo.ProjectID, maxRepositories); err != nil {
			return err
		}
	}

	repoType := repo.RepoType
	if repoType == "" {
		repoType = "hosted"
	}

	err = tx.QueryRowContext(ctx,
		`INSERT INTO repositories (id, name, format, description, public, repo_type, upstream_url, upstream_username, upstream_password_enc, cache_ttl_seconds, negative_cache_ttl_seconds, max_stale_seconds, blocked, project_id, presign_downloads, trash_retention_days, immutable_releases, merge_proxy_versions) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id, created_at, updated_at`,
		repo.Name, repo.Format, repo.Description, repo.Public, repoType, repo.UpstreamURL, repo.UpstreamUsername, repo.UpstreamPasswordEnc, repo.CacheTTLSeconds, repo.NegativeCacheTTLSeconds, repo.MaxStaleSeconds, repo.Blocked, repo.ProjectID, repo.PresignDownloads, repo.TrashRetentionDays, repo.ImmutableReleases, repo.MergeProxyVersions,
	).Scan(&repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}

	if err := insertGroupMembers(ctx, tx, repo.ID, memberIDs); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit repository create: %w", err)
	}

	return nil
}

// ReplaceGroupMembers atomically replaces the member list of a group
// repository. An empty memberIDs slice clears the group.
func (p *PostgresDB) ReplaceGroupMembers(ctx context.Context, groupRepoID string, memberIDs []string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := replaceGroupMembersTx(ctx, tx, groupRepoID, memberIDs); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit group members: %w", err)
	}

	return nil
}

// UpdateRepositoryAndMembers updates the repository row and replaces its group
// members in one transaction, so a failure of either leaves neither applied.
func (p *PostgresDB) UpdateRepositoryAndMembers(ctx context.Context, repo *models.Repository, memberIDs []string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := updateRepositoryRow(ctx, tx, repo); err != nil {
		return err
	}

	if err := replaceGroupMembersTx(ctx, tx, repo.ID, memberIDs); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit repository update: %w", err)
	}

	return nil
}

func replaceGroupMembersTx(ctx context.Context, tx *sql.Tx, groupRepoID string, memberIDs []string) error {
	// Members sitting in the trash are invisible to the caller (ListGroupMembers
	// skips them), so they are kept and rejoin the group on restore.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM repository_group_members m WHERE m.group_repo_id = $1
		   AND NOT EXISTS (SELECT 1 FROM repositories r WHERE r.id = m.member_repo_id AND r.deleted_at IS NOT NULL)`,
		groupRepoID); err != nil {
		return fmt.Errorf("failed to clear group members: %w", err)
	}

	// Kept members rank after the new list so their old priorities cannot
	// collide with it; on restore they rejoin at the end in their old order.
	if _, err := tx.ExecContext(ctx,
		`UPDATE repository_group_members m SET priority = $2 + k.rn - 1
		   FROM (SELECT member_repo_id, row_number() OVER (ORDER BY priority, created_at, member_repo_id) AS rn
		           FROM repository_group_members WHERE group_repo_id = $1) k
		  WHERE m.group_repo_id = $1 AND m.member_repo_id = k.member_repo_id`,
		groupRepoID, len(memberIDs)); err != nil {
		return fmt.Errorf("failed to renumber kept group members: %w", err)
	}

	return insertGroupMembers(ctx, tx, groupRepoID, memberIDs)
}

func insertGroupMembers(ctx context.Context, tx *sql.Tx, groupRepoID string, memberIDs []string) error {
	for priority, memberID := range memberIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO repository_group_members (group_repo_id, member_repo_id, priority) VALUES ($1, $2, $3)`,
			groupRepoID, memberID, priority,
		); err != nil {
			return fmt.Errorf("failed to add group member: %w", err)
		}
	}

	return nil
}

// escapeLike escapes LIKE/ILIKE metacharacters so user input matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// UniqueViolationConstraint returns the violated constraint/index name when
// err is a unique_violation, and "" otherwise.
func UniqueViolationConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return pgErr.ConstraintName
	}

	return ""
}

// CountBlobsOnStorageBackend returns how many blobs are recorded on a storage backend.
func (p *PostgresDB) CountBlobsOnStorageBackend(ctx context.Context, backendID string) (int64, error) {
	var n int64

	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs WHERE storage_backend_id = $1`, backendID).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count blobs on storage backend: %w", err)
	}

	return n, nil
}
