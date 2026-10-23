// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
		PartSize:     u.PartSize,
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
		CompletingSince: row.CompletingSince,
		CompletedAt:     row.CompletedAt,
		ArtifactID:      row.ArtifactID,
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Path:            row.Path,
		SizeBytes:       row.SizeBytes,
		SHA256:          row.Sha256,
		ContentType:     row.ContentType,
		CreatedBy:       row.CreatedBy,
		CreatedAt:       row.CreatedAt,
		ExpiresAt:       row.ExpiresAt,
		PartSize:        row.PartSize,
		MultipartID:     row.MultipartID,
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

// ClaimPresignedUpload takes the complete lease of an upload for up to lease.
// It reports false when the upload is already completed, does not exist, or
// another complete holds an unexpired lease.
func (p *PostgresDB) ClaimPresignedUpload(ctx context.Context, repoID, id string, lease time.Duration) (bool, error) {
	if !isUUID(id) {
		return false, nil
	}

	n, err := p.q.ClaimPresignedUpload(ctx, dbsqlc.ClaimPresignedUploadParams{ID: id, RepositoryID: repoID, LeaseSeconds: lease.Seconds()})
	if err != nil {
		return false, fmt.Errorf("failed to claim presigned upload: %w", err)
	}

	return n == 1, nil
}

// ReleasePresignedUpload gives up the lease of an upload that was not completed.
func (p *PostgresDB) ReleasePresignedUpload(ctx context.Context, id string) error {
	if !isUUID(id) {
		return nil
	}

	if err := p.q.ReleasePresignedUpload(ctx, id); err != nil {
		return fmt.Errorf("failed to release presigned upload: %w", err)
	}

	return nil
}

// MarkPresignedUploadCompleted records that the upload produced artifactID.
func (p *PostgresDB) MarkPresignedUploadCompleted(ctx context.Context, id, artifactID string) error {
	if !isUUID(id) {
		return nil
	}

	if err := p.q.MarkPresignedUploadCompleted(ctx, dbsqlc.MarkPresignedUploadCompletedParams{ID: id, ArtifactID: artifactID}); err != nil {
		return fmt.Errorf("failed to mark presigned upload completed: %w", err)
	}

	return nil
}

// ErrPresignedUploadCap is returned by CreatePresignedUploadCapped when the
// user already has the maximum number of outstanding uploads.
var ErrPresignedUploadCap = errors.New("too many pending presigned uploads")

// CreatePresignedUploadCapped counts the user's outstanding uploads and inserts
// the new one in one transaction holding a per-user advisory lock, so parallel
// creates cannot all pass the check before any of them inserts.
func (p *PostgresDB) CreatePresignedUploadCapped(ctx context.Context, u *models.PresignedUpload, maxPending int) error {
	if maxPending <= 0 {
		return p.CreatePresignedUpload(ctx, u)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin presigned upload transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "presigned-upload:"+u.CreatedBy); err != nil {
		return fmt.Errorf("lock presigned uploads of user: %w", err)
	}

	var pending int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM presigned_uploads WHERE created_by = $1 AND completed_at IS NULL AND expires_at > now()`,
		u.CreatedBy).Scan(&pending); err != nil {
		return fmt.Errorf("count pending presigned uploads: %w", err)
	}

	if pending >= maxPending {
		return ErrPresignedUploadCap
	}

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO presigned_uploads (repository_id, path, size_bytes, sha256, content_type, created_by, expires_at, part_size)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		u.RepositoryID, u.Path, u.SizeBytes, u.SHA256, u.ContentType, u.CreatedBy, u.ExpiresAt, u.PartSize,
	).Scan(&u.ID, &u.CreatedAt); err != nil {
		return fmt.Errorf("failed to create presigned upload: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit presigned upload: %w", err)
	}

	return nil
}

// ExistingPresignedUploadIDs returns the subset of ids that have a row. Ids
// that are not UUIDs cannot have one and are dropped.
func (p *PostgresDB) ExistingPresignedUploadIDs(ctx context.Context, ids []string) ([]string, error) {
	valid := make([]string, 0, len(ids))

	for _, id := range ids {
		if isUUID(id) {
			valid = append(valid, id)
		}
	}

	if len(valid) == 0 {
		return nil, nil
	}

	found, err := p.q.ExistingPresignedUploadIDs(ctx, valid)
	if err != nil {
		return nil, fmt.Errorf("failed to look up presigned uploads: %w", err)
	}

	return found, nil
}

// SetPresignedUploadMultipartID records the backend handle of the upload's open multipart upload.
func (p *PostgresDB) SetPresignedUploadMultipartID(ctx context.Context, id, multipartID string) error {
	if !isUUID(id) {
		return nil
	}

	if err := p.q.SetPresignedUploadMultipartID(ctx, dbsqlc.SetPresignedUploadMultipartIDParams{ID: id, MultipartID: multipartID}); err != nil {
		return fmt.Errorf("failed to set presigned upload multipart id: %w", err)
	}

	return nil
}

// ExtendPresignedUploadExpiry moves the complete deadline of a pending upload
// forward to expiresAt; it never shortens it.
func (p *PostgresDB) ExtendPresignedUploadExpiry(ctx context.Context, id string, expiresAt time.Time) error {
	if !isUUID(id) {
		return nil
	}

	if err := p.q.ExtendPresignedUploadExpiry(ctx, dbsqlc.ExtendPresignedUploadExpiryParams{ID: id, ExpiresAt: expiresAt}); err != nil {
		return fmt.Errorf("failed to extend presigned upload expiry: %w", err)
	}

	return nil
}
