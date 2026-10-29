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

// RepoMetadataStore owns repository_metadata, the generated per-repository metadata files (Packages, repomd.xml, maven-metadata.xml, ...).
type RepoMetadataStore struct{ *core }

func (p *RepoMetadataStore) GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error) {
	m := &models.RepoMetadata{}

	err := p.db.QueryRowContext(
		ctx,
		`SELECT id, repository_id, path, blob_sha256, blob_size, last_generated FROM repository_metadata WHERE repository_id = $1 AND path = $2`,
		repositoryID,
		path,
	).Scan(&m.ID, &m.RepositoryID, &m.Path, &m.BlobSHA256, &m.BlobSize, &m.LastGenerated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get repo metadata by path: %w", err)
	}

	return m, nil
}

func (p *RepoMetadataStore) GetRepoMetadataByRepo(ctx context.Context, repositoryID string) ([]models.RepoMetadata, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, repository_id, path, blob_sha256, blob_size, last_generated FROM repository_metadata WHERE repository_id = $1`,
		repositoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query repo metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var metas []models.RepoMetadata

	for rows.Next() {
		var m models.RepoMetadata
		if err := rows.Scan(&m.ID, &m.RepositoryID, &m.Path, &m.BlobSHA256, &m.BlobSize, &m.LastGenerated); err != nil {
			return nil, fmt.Errorf("failed to scan repo metadata: %w", err)
		}

		metas = append(metas, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate repo metadata: %w", err)
	}

	return metas, nil
}

func (p *RepoMetadataStore) UpsertRepoMetadata(ctx context.Context, meta *models.RepoMetadata) error {
	return p.db.QueryRowContext(
		ctx,
		`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, $2, $3, $4) ON CONFLICT
			(repository_id, path) DO UPDATE SET blob_sha256 = $3, blob_size = $4, last_generated = now() RETURNING id,
			last_generated`,
		meta.RepositoryID,
		meta.Path,
		meta.BlobSHA256,
		meta.BlobSize,
	).Scan(&meta.ID, &meta.LastGenerated)
}
