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

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// RawStore owns the raw (generic file) artifacts.
type RawStore struct{ *core }

// UpsertRawArtifact inserts or updates a raw artifact.
func (p *RawStore) UpsertRawArtifact(ctx context.Context, artifact *models.RawArtifact) error {
	_, err := p.writeRawArtifact(ctx, artifact, false)

	return err
}

// InsertRawArtifactIfAbsent inserts a raw artifact only when no live row
// exists at its path, in one atomic statement. It reports whether the row was
// inserted; false means the path is taken and nothing was changed.
func (p *RawStore) InsertRawArtifactIfAbsent(ctx context.Context, artifact *models.RawArtifact) (bool, error) {
	return p.writeRawArtifact(ctx, artifact, true)
}

func (p *RawStore) writeRawArtifact(ctx context.Context, artifact *models.RawArtifact, ifAbsent bool) (bool, error) {
	metadataJSON, err := json.Marshal(artifact.Metadata)
	if err != nil {
		return false, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	blobID, err := p.upsertBlobWithMediaType(ctx, artifact.SHA256, artifact.BlobKey, artifact.ContentType, artifact.SizeBytes)
	if err != nil {
		return false, fmt.Errorf("failed to upsert blob for raw artifact: %w", err)
	}

	// Callers (the raw PUT handler) leave the timestamps unset; storing the
	// zero time made every raw artifact show as uploaded on 0001-01-01.
	now := time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}

	if artifact.UpdatedAt.IsZero() {
		artifact.UpdatedAt = now
	}

	inserted := true

	if ifAbsent {
		n, err := p.q.InsertRawArtifactIfAbsent(ctx, dbsqlc.InsertRawArtifactIfAbsentParams{
			ID:           artifact.ID,
			RepositoryID: artifact.RepositoryID,
			Path:         artifact.Path,
			ContentType:  artifact.ContentType,
			BlobID:       blobID,
			Metadata:     json.RawMessage(metadataJSON),
			CreatedAt:    artifact.CreatedAt,
			UpdatedAt:    artifact.UpdatedAt,
		})
		if err != nil {
			return false, fmt.Errorf("failed to insert raw artifact: %w", err)
		}

		inserted = n > 0
	} else if err := p.q.UpsertRawArtifact(ctx, dbsqlc.UpsertRawArtifactParams{
		ID:           artifact.ID,
		RepositoryID: artifact.RepositoryID,
		Path:         artifact.Path,
		ContentType:  artifact.ContentType,
		BlobID:       blobID,
		Metadata:     json.RawMessage(metadataJSON),
		CreatedAt:    artifact.CreatedAt,
		UpdatedAt:    artifact.UpdatedAt,
	}); err != nil {
		return false, fmt.Errorf("failed to upsert raw artifact: %w", err)
	}

	artifact.BlobID = blobID

	return inserted, nil
}

// GetRawArtifact retrieves a raw artifact by repo ID and path.
func (p *RawStore) GetRawArtifact(ctx context.Context, repoID, path string) (*models.RawArtifact, error) {
	row, err := p.q.GetRawArtifact(ctx, dbsqlc.GetRawArtifactParams{
		RepositoryID: repoID,
		Path:         path,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get raw artifact: %w", err)
	}

	var metadata map[string]any
	if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &models.RawArtifact{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Path:         row.Path,
		ContentType:  row.ContentType,
		BlobID:       row.BlobID,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		BlobKey:      row.BlobKey,
		Metadata:     metadata,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// DeleteRawArtifact deletes a raw artifact by repo ID and path.
func (p *RawStore) DeleteRawArtifact(ctx context.Context, repoID, path string) error {
	if err := p.q.DeleteRawArtifact(ctx, dbsqlc.DeleteRawArtifactParams{
		RepositoryID: repoID,
		Path:         path,
	}); err != nil {
		return fmt.Errorf("failed to delete raw artifact: %w", err)
	}

	return nil
}

// ListRawArtifacts lists raw artifacts in a repository, optionally filtered by prefix.
func (p *RawStore) ListRawArtifacts(ctx context.Context, repoID, prefix string) ([]*models.RawArtifact, error) {
	var dbRows []dbsqlc.ListRawArtifactsRow

	var err error

	if prefix == "" {
		dbRows, err = p.q.ListRawArtifacts(ctx, repoID)
	} else {
		prefixRows, prefixErr := p.q.ListRawArtifactsByPrefix(ctx, dbsqlc.ListRawArtifactsByPrefixParams{
			RepositoryID: repoID,
			Path:         escapeLike(prefix) + "%",
		})

		err = prefixErr
		dbRows = make([]dbsqlc.ListRawArtifactsRow, len(prefixRows))

		for i, r := range prefixRows {
			dbRows[i] = dbsqlc.ListRawArtifactsRow(r)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list raw artifacts: %w", err)
	}

	artifacts := make([]*models.RawArtifact, 0, len(dbRows))

	for _, row := range dbRows {
		var metadata map[string]any
		if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}

		artifacts = append(artifacts, &models.RawArtifact{
			ID:           row.ID,
			RepositoryID: row.RepositoryID,
			Path:         row.Path,
			ContentType:  row.ContentType,
			BlobID:       row.BlobID,
			SizeBytes:    row.SizeBytes,
			SHA256:       row.Sha256,
			BlobKey:      row.BlobKey,
			Metadata:     metadata,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}

	return artifacts, nil
}
