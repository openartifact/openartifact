// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// IvyStore owns the Ivy artifacts.
type IvyStore struct{ *core }

// CreateIvyArtifact stores an Ivy artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *IvyStore) CreateIvyArtifact(ctx context.Context, artifact *models.IvyArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for ivy artifact: %w", err)
	}

	row, err := p.q.CreateIvyArtifact(ctx, dbsqlc.CreateIvyArtifactParams{
		CreatedAt:    createdAtOrNil(artifact.CreatedAt),
		RepositoryID: artifact.RepositoryID,
		Organisation: artifact.Organisation,
		Module:       artifact.Module,
		Revision:     artifact.Revision,
		ArtifactName: artifact.ArtifactName,
		ArtifactType: artifact.ArtifactType,
		ArtifactExt:  artifact.ArtifactExt,
		BlobID:       blobID,
	})

	return finishCreate(insertedRow(row), err, "create ivy artifact", &artifact.ID, &artifact.CreatedAt, &artifact.UpdatedAt)
}

func (p *IvyStore) GetIvyArtifact(
	ctx context.Context,
	repoID, organisation, module, revision, artifactName, artifactExt string,
) (*models.IvyArtifact, error) {
	row, err := p.q.GetIvyArtifact(ctx, dbsqlc.GetIvyArtifactParams{
		RepositoryID: repoID,
		Organisation: organisation,
		Module:       module,
		Revision:     revision,
		ArtifactName: artifactName,
		ArtifactExt:  artifactExt,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get ivy artifact: %w", err)
	}

	return &models.IvyArtifact{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Organisation: row.Organisation,
		Module:       row.Module,
		Revision:     row.Revision,
		ArtifactName: row.ArtifactName,
		ArtifactType: row.ArtifactType,
		ArtifactExt:  row.ArtifactExt,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (p *IvyStore) DeleteIvyArtifact(ctx context.Context, id string) error {
	if err := p.q.DeleteIvyArtifact(ctx, id); err != nil {
		return fmt.Errorf("failed to delete ivy artifact: %w", err)
	}

	return nil
}

func (p *IvyStore) ListIvyArtifacts(ctx context.Context, repoID string) ([]*models.IvyArtifact, error) {
	rows, err := p.q.ListIvyArtifacts(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to query ivy artifacts: %w", err)
	}

	var artifacts []*models.IvyArtifact

	for _, row := range rows {
		artifacts = append(artifacts, &models.IvyArtifact{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Organisation: row.Organisation,
			Module:       row.Module,
			Revision:     row.Revision,
			ArtifactName: row.ArtifactName,
			ArtifactType: row.ArtifactType,
			ArtifactExt:  row.ArtifactExt,
			BlobKey:      row.BlobKey,
			SizeBytes:    row.SizeBytes,
			SHA256:       row.Sha256,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}

	return artifacts, nil
}

// IvyModuleExists reports whether the repository holds a live file of the module.
func (p *IvyStore) IvyModuleExists(ctx context.Context, repoID, organisation, module string) (bool, error) {
	exists, err := p.q.IvyModuleExists(ctx, dbsqlc.IvyModuleExistsParams{RepositoryID: repoID, Organisation: organisation, Module: module})
	if err != nil {
		return false, fmt.Errorf("failed to check ivy module: %w", err)
	}

	return exists, nil
}
