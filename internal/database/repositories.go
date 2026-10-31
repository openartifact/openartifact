// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openartifact/openartifact/internal/curation"

	"github.com/openartifact/openartifact/internal/database/models"
)

// RepositoryStore owns the repositories table: lookup, create/update/delete, the repository flags and the project move.
type RepositoryStore struct{ *core }

// GetRepositoryByName returns the repository with the given name, from the
// auth cache when fresh.
func (p *RepositoryStore) GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error) {
	return cachedPtr(ctx, &p.authCache, &p.authCache.repos, name, cloneShallow, p.GetRepositoryByNameUncached)
}

func (p *RepositoryStore) GetRepositoryByNameUncached(ctx context.Context, name string) (*models.Repository, error) {
	return p.getRepositoryWhere(ctx, "name", "name = $1", name)
}

// getRepositoryWhere returns the live repository matching the WHERE clause, or nil when there is none.
func (p *RepositoryStore) getRepositoryWhere(ctx context.Context, what, where string, arg any) (*models.Repository, error) {
	r, err := scanRepository(
		p.db.QueryRowContext(ctx, `SELECT `+repoColumns+` FROM repositories WHERE `+where+` AND deleted_at IS NULL`, arg),
	)

	return orNotFound(r, err, "get repository by "+what)
}

// ListPublicRepositoryNames returns the names of all repositories flagged public.
func (p *RepositoryStore) ListPublicRepositoryNames(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT name FROM repositories WHERE COALESCE(public, false) AND deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("failed to list public repositories: %w", err)
	}

	return collectRows(rows, "failed to scan public repository", "failed to iterate public repositories", scanString)
}

// GetRepositoryByID retrieves a repository by its primary key.
func (p *RepositoryStore) GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error) {
	return p.getRepositoryWhere(ctx, "id", "id = $1", id)
}

func (p *RepositoryStore) ListRepositories(ctx context.Context, page, limit int, formatFilter string) ([]*models.Repository, error) {
	offset := (page - 1) * limit

	var (
		rows *sql.Rows
		err  error
	)
	if formatFilter != "" {
		rows, err = p.db.QueryContext(
			ctx,
			`SELECT `+repoColumns+` FROM repositories WHERE format = $1 AND deleted_at IS NULL ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`,
			formatFilter,
			limit,
			offset,
		)
	} else {
		rows, err = p.db.QueryContext(ctx,
			`SELECT `+repoColumns+` FROM repositories WHERE deleted_at IS NULL ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`,
			limit, offset,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list repositories: %w", err)
	}

	return collectRows(rows, "failed to scan repository", "failed to iterate repositories", scanRepository)
}

func (p *RepositoryStore) CountRepositories(ctx context.Context, formatFilter string) (int, error) {
	var (
		count int
		err   error
	)
	if formatFilter != "" {
		err = p.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM repositories WHERE format = $1 AND deleted_at IS NULL`, formatFilter,
		).Scan(&count)
	} else {
		err = p.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM repositories WHERE deleted_at IS NULL`,
		).Scan(&count)
	}

	if err != nil {
		return 0, fmt.Errorf("failed to count repositories: %w", err)
	}

	return count, nil
}

func (p *RepositoryStore) UpdateRepository(ctx context.Context, repo *models.Repository) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	return updateRepositoryRow(ctx, p.db, repo)
}

// repoExecer is the part of *sql.DB and *sql.Tx the repository update needs.
type repoExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func updateRepositoryRow(ctx context.Context, db repoExecer, repo *models.Repository) error {
	_, err := db.ExecContext(
		ctx,
		`UPDATE repositories SET description = $1, public = $2, repo_type = $3, upstream_url = NULLIF($4,''), upstream_username =
			NULLIF($5,''), upstream_password_enc = NULLIF($6,''), cache_ttl_seconds = $7, negative_cache_ttl_seconds = $8,
			max_stale_seconds = $9, blocked = $10, project_id = $11, trash_retention_days = $12,
			immutable_releases = $13, presign_downloads = $15, merge_proxy_versions = $16, sign_packages = $17, updated_at = now(),
			min_age_hours = $18, curation_mode = $19 WHERE id = $14 AND deleted_at IS NULL`,
		repo.Description,
		repo.Public,
		repo.RepoType,
		repo.UpstreamURL,
		repo.UpstreamUsername,
		repo.UpstreamPasswordEnc,
		repo.CacheTTLSeconds,
		repo.NegativeCacheTTLSeconds,
		repo.MaxStaleSeconds,
		repo.Blocked,
		repo.ProjectID,
		repo.TrashRetentionDays,
		repo.ImmutableReleases,
		repo.ID,
		repo.PresignDownloads,
		repo.MergeProxyVersions,
		repo.SignPackages,
		repo.MinAgeHours,
		curationModeOrOff(repo.CurationMode),
	)
	if err != nil {
		return fmt.Errorf("failed to update repository: %w", err)
	}

	return nil
}

func (p *RepositoryStore) DeleteRepository(ctx context.Context, id string) error {
	// Cached auth lookups must not outlive this write.
	defer p.authCache.invalidate()

	_, err := p.db.ExecContext(ctx, `DELETE FROM repositories WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete repository: %w", err)
	}

	return nil
}

func (p *RepositoryStore) CreateRepository(ctx context.Context, repo *models.Repository) error {
	// A new repository joins the readable set of whoever's grants match it.
	defer p.authCache.invalidate()

	repoType := repo.RepoType
	if repoType == "" {
		repoType = "hosted"
	}

	return p.db.QueryRowContext(
		ctx,
		`INSERT INTO repositories (id, name, format, description, public, repo_type, upstream_url, upstream_username,
			upstream_password_enc, cache_ttl_seconds, negative_cache_ttl_seconds, max_stale_seconds, min_age_hours, curation_mode,
			blocked, project_id, trash_retention_days, immutable_releases, presign_downloads, allow_full_unpublish, merge_proxy_versions,
			sign_packages) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10, $11,
			$12, $13, $14, $15, $16, $17, $18, $19, $20, $21) RETURNING id, created_at, updated_at`,
		repo.Name,
		repo.Format,
		repo.Description,
		repo.Public,
		repoType,
		repo.UpstreamURL,
		repo.UpstreamUsername,
		repo.UpstreamPasswordEnc,
		repo.CacheTTLSeconds,
		repo.NegativeCacheTTLSeconds,
		repo.MaxStaleSeconds,
		repo.MinAgeHours,
		curationModeOrOff(repo.CurationMode),
		repo.Blocked,
		repo.ProjectID,
		repo.TrashRetentionDays,
		repo.ImmutableReleases,
		repo.PresignDownloads,
		repo.AllowFullUnpublish,
		repo.MergeProxyVersions,
		repo.SignPackages,
	).Scan(&repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
}

// curationModeOrOff is mode, or "off" for a model that never set it.
func curationModeOrOff(mode string) string {
	if mode == "" {
		return curation.ModeOff
	}

	return mode
}
