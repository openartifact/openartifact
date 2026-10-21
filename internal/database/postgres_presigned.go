// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// CreatePresignedUpload records a presigned upload and fills u.ID and u.CreatedAt.
func (p *PostgresDB) CreatePresignedUpload(ctx context.Context, u *models.PresignedUpload) error {
	row, err := p.q.CreatePresignedUpload(ctx, dbsqlc.CreatePresignedUploadParams{
		RepositoryID: u.RepositoryID,
		Path:         u.Path,
		SizeBytes:    u.SizeBytes,
		Sha256:       u.SHA256,
		ContentType:  u.ContentType,
		CreatedBy:    u.CreatedBy,
		ExpiresAt:    u.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("failed to create presigned upload: %w", err)
	}

	u.ID, u.CreatedAt = row.ID, row.CreatedAt

	return nil
}

// GetPresignedUpload returns the presigned upload id of repository repoID, or
// nil, nil when there is none.
func (p *PostgresDB) GetPresignedUpload(ctx context.Context, repoID, id string) (*models.PresignedUpload, error) {
	if !isUUID(id) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	row, err := p.q.GetPresignedUpload(ctx, dbsqlc.GetPresignedUploadParams{ID: id, RepositoryID: repoID})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil, nil is the standard Go "not found" pattern
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get presigned upload: %w", err)
	}

	return &models.PresignedUpload{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Path:         row.Path,
		SizeBytes:    row.SizeBytes,
		SHA256:       row.Sha256,
		ContentType:  row.ContentType,
		CreatedBy:    row.CreatedBy,
		CreatedAt:    row.CreatedAt,
		ExpiresAt:    row.ExpiresAt,
	}, nil
}

// DeletePresignedUpload removes a presigned upload row.
func (p *PostgresDB) DeletePresignedUpload(ctx context.Context, id string) error {
	if !isUUID(id) {
		return nil
	}

	if err := p.q.DeletePresignedUpload(ctx, id); err != nil {
		return fmt.Errorf("failed to delete presigned upload: %w", err)
	}

	return nil
}

// ListExpiredPresignedUploads returns up to limit ids of presigned uploads
// past their complete deadline.
func (p *PostgresDB) ListExpiredPresignedUploads(ctx context.Context, limit int) ([]string, error) {
	ids, err := p.q.ListExpiredPresignedUploads(ctx, int32(limit)) //nolint:gosec // limit is a small constant batch size
	if err != nil {
		return nil, fmt.Errorf("failed to list expired presigned uploads: %w", err)
	}

	return ids, nil
}
