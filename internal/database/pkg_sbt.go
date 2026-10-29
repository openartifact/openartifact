// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// SBTStore owns the sbt artifacts.
type SBTStore struct{ *core }

// SBTModuleExists reports whether the repository holds a live file of the artifact.
func (p *SBTStore) SBTModuleExists(ctx context.Context, repoID, groupID, artifactID string) (bool, error) {
	exists, err := p.q.SBTModuleExists(ctx, dbsqlc.SBTModuleExistsParams{RepositoryID: repoID, GroupID: groupID, ArtifactID: artifactID})
	if err != nil {
		return false, fmt.Errorf("failed to check sbt artifact: %w", err)
	}

	return exists, nil
}

// CreateSBTArtifact stores an SBT artifact; a live artifact with the same
// coordinates is repointed at the new blob.
func (p *SBTStore) CreateSBTArtifact(ctx context.Context, artifact *models.SBTArtifact) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, "application/java-archive", artifact.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for sbt artifact: %w", err)
	}

	row, err := p.q.CreateSBTArtifact(ctx, dbsqlc.CreateSBTArtifactParams{
		CreatedAt:    createdAtOrNil(artifact.CreatedAt),
		RepositoryID: artifact.RepositoryID,
		GroupID:      artifact.GroupID,
		ArtifactID:   artifact.ArtifactID,
		Version:      artifact.Version,
		Filename:     artifact.Filename,
		BlobID:       blobID,
	})

	return finishCreate(insertedRow(row), err, "create sbt artifact", &artifact.ID, &artifact.CreatedAt, &artifact.UpdatedAt)
}

func (p *SBTStore) GetSBTArtifact(
	ctx context.Context,
	repoID, groupID, artifactID, version, filename string,
) (*models.SBTArtifact, error) {
	row, err := p.q.GetSBTArtifact(ctx, dbsqlc.GetSBTArtifactParams{
		RepositoryID: repoID,
		GroupID:      groupID,
		ArtifactID:   artifactID,
		Version:      version,
		Filename:     filename,
	})

	return mapRow(row, err, "get sbt artifact", sbtArtifactFromRow)
}

func sbtArtifactFromRow(row dbsqlc.GetSBTArtifactRow) *models.SBTArtifact {
	return &models.SBTArtifact{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		GroupID:      row.GroupID,
		ArtifactID:   row.ArtifactID,
		Version:      row.Version,
		Filename:     row.Filename,
		BlobKey:      row.BlobKey,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func (p *SBTStore) DeleteSBTArtifact(ctx context.Context, id string) error {
	if err := p.q.DeleteSBTArtifact(ctx, id); err != nil {
		return fmt.Errorf("failed to delete sbt artifact: %w", err)
	}

	return nil
}

func (p *SBTStore) ListSBTArtifacts(ctx context.Context, repoID string) ([]*models.SBTArtifact, error) {
	rows, err := p.q.ListSBTArtifacts(ctx, repoID)

	return mapRowsOrNil(rows, err, "query sbt artifacts", func(r dbsqlc.ListSBTArtifactsRow) *models.SBTArtifact {
		return sbtArtifactFromRow(dbsqlc.GetSBTArtifactRow(r))
	})
}
