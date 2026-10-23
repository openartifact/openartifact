// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// DockerCatalog is implemented by databases that can list the images of every
// docker repository for the registry's /v2/_catalog.
type DockerCatalog interface {
	// ListDockerCatalog returns up to limit catalog names after the given one
	// ("" starts at the beginning), ordered bytewise by distribution name.
	ListDockerCatalog(ctx context.Context, after string, limit int) ([]*models.DockerCatalogEntry, error)
}

var _ DockerCatalog = (*PostgresDB)(nil)

func (p *PostgresDB) ListDockerCatalog(ctx context.Context, after string, limit int) ([]*models.DockerCatalogEntry, error) {
	rows, err := p.q.ListDockerCatalog(ctx, dbsqlc.ListDockerCatalogParams{After: after, PageLimit: toInt32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list docker catalog: %w", err)
	}

	out := make([]*models.DockerCatalogEntry, 0, len(rows))

	for _, row := range rows {
		if len(row.SourceIds) != len(row.SourceNames) || len(row.SourceIds) != len(row.SourceProjectIds) {
			return nil, fmt.Errorf("list docker catalog: %w", errCatalogSourcesMismatch)
		}

		entry := &models.DockerCatalogEntry{
			Name:       row.FullName,
			Repository: catalogRepository(row.RepositoryID, row.RepositoryName, row.RepositoryProjectID),
			Image:      row.Image,
			Sources:    make([]*models.Repository, len(row.SourceIds)),
		}

		for i := range row.SourceIds {
			entry.Sources[i] = catalogRepository(row.SourceIds[i], row.SourceNames[i], row.SourceProjectIds[i])
		}

		out = append(out, entry)
	}

	return out, nil
}

// catalogRepository is the part of a repository the permission evaluator reads.
func catalogRepository(id, name, projectID string) *models.Repository {
	repo := &models.Repository{ID: id, Name: name}
	if projectID != "" {
		repo.ProjectID = &projectID
	}

	return repo
}

// DockerBlobTrash is implemented by databases that can tell whether a blob is
// held only by trashed manifests.
type DockerBlobTrash interface {
	// DockerBlobInTrash reports whether manifests of the repository reference
	// the blob and every one of them is in the trash.
	DockerBlobInTrash(ctx context.Context, repoID, digest string) (bool, error)
}

var _ DockerBlobTrash = (*PostgresDB)(nil)

func (p *PostgresDB) DockerBlobInTrash(ctx context.Context, repoID, digest string) (bool, error) {
	trashed, err := p.q.DockerBlobHeldOnlyByTrash(ctx, dbsqlc.DockerBlobHeldOnlyByTrashParams{RepositoryID: repoID, Digest: digest})
	if err != nil {
		return false, fmt.Errorf("check docker blob in trash: %w", err)
	}

	return trashed, nil
}

var errCatalogSourcesMismatch = errors.New("catalog entry source lists differ in length")
