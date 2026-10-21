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
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	repoType := repo.RepoType
	if repoType == "" {
		repoType = "hosted"
	}

	err = tx.QueryRowContext(ctx,
		`INSERT INTO repositories (id, name, format, description, public, repo_type, upstream_url, upstream_username, upstream_password_enc, cache_ttl_seconds, negative_cache_ttl_seconds, max_stale_seconds, blocked, org_id, presign_downloads) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10, $11, $12, $13, $14) RETURNING id, created_at, updated_at`,
		repo.Name, repo.Format, repo.Description, repo.Public, repoType, repo.UpstreamURL, repo.UpstreamUsername, repo.UpstreamPasswordEnc, repo.CacheTTLSeconds, repo.NegativeCacheTTLSeconds, repo.MaxStaleSeconds, repo.Blocked, repo.OrgID, repo.PresignDownloads,
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM repository_group_members WHERE group_repo_id = $1`, groupRepoID); err != nil {
		return fmt.Errorf("failed to clear group members: %w", err)
	}

	if err := insertGroupMembers(ctx, tx, groupRepoID, memberIDs); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit group members: %w", err)
	}

	return nil
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
