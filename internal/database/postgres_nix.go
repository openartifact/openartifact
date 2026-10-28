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

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// nixMediaType is the media type of a stored NAR file.
const nixMediaType = "application/x-nix-nar"

func nixPathFromRow(row dbsqlc.GetNixPathByHashRow) *models.NixPath {
	return &models.NixPath{
		ID:            row.ID,
		RepositoryID:  row.RepositoryID,
		StoreHash:     row.StoreHash,
		Name:          row.Name,
		NarHash:       row.NarHash,
		NarSize:       row.NarSize,
		NarFile:       row.NarFile,
		Compression:   row.Compression,
		References:    row.Refs,
		Deriver:       row.Deriver,
		CA:            row.Ca,
		Signatures:    row.Signatures,
		DownloadCount: row.DownloadCount,
		BlobKey:       row.BlobKey,
		SHA256:        row.BlobSha256,
		SizeBytes:     row.SizeBytes,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

// PutNixPendingNar stores a NAR file that awaits its narinfo. It is idempotent
// per (repository, sha256).
func (p *PostgresDB) PutNixPendingNar(ctx context.Context, n *models.NixPendingNar) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, n.SHA256, n.BlobKey, nixMediaType, n.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for nix nar: %w", err)
	}

	err = p.q.PutNixPendingNar(ctx, dbsqlc.PutNixPendingNarParams{
		RepositoryID: n.RepositoryID, Sha256: n.SHA256, NarFile: n.NarFile, BlobID: blobID,
	})
	if err != nil {
		return fmt.Errorf("failed to stage nix nar: %w", err)
	}

	return nil
}

// GetNixPendingNar returns the staged NAR file, or (nil, nil).
func (p *PostgresDB) GetNixPendingNar(ctx context.Context, repoID, sha256 string) (*models.NixPendingNar, error) {
	row, err := p.q.GetNixPendingNar(ctx, dbsqlc.GetNixPendingNarParams{RepositoryID: repoID, Sha256: sha256})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get nix pending nar: %w", err)
	}

	return &models.NixPendingNar{
		RepositoryID: row.RepositoryID, SHA256: row.Sha256, NarFile: row.NarFile,
		BlobKey: row.BlobKey, SizeBytes: row.SizeBytes, CreatedAt: row.CreatedAt,
	}, nil
}

// GetNixPathByNarFile returns a live path whose NAR file is narFile, or (nil, nil).
func (p *PostgresDB) GetNixPathByNarFile(ctx context.Context, repoID, narFile string) (*models.NixPath, error) {
	row, err := p.q.GetNixPathByNarFile(ctx, dbsqlc.GetNixPathByNarFileParams{RepositoryID: repoID, NarFile: narFile})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get nix path by nar file: %w", err)
	}

	return nixPathFromRow(dbsqlc.GetNixPathByHashRow(row)), nil
}

// GetNixPathByHash returns the live path of the store hash, or (nil, nil).
func (p *PostgresDB) GetNixPathByHash(ctx context.Context, repoID, storeHash string) (*models.NixPath, error) {
	row, err := p.q.GetNixPathByHash(ctx, dbsqlc.GetNixPathByHashParams{RepositoryID: repoID, StoreHash: storeHash})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get nix path: %w", err)
	}

	return nixPathFromRow(row), nil
}

// CreateNixPath stores the path and drops the staging row of its NAR file in
// one transaction. A live row with the same store hash is a unique violation
// (IsUniqueViolation).
func (p *PostgresDB) CreateNixPath(ctx context.Context, np *models.NixPath) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, np.SHA256, np.BlobKey, nixMediaType, np.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for nix path: %w", err)
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := p.q.WithTx(tx)

	row, err := q.CreateNixPath(ctx, dbsqlc.CreateNixPathParams{
		RepositoryID: np.RepositoryID,
		StoreHash:    np.StoreHash,
		Name:         np.Name,
		NarHash:      np.NarHash,
		NarSize:      np.NarSize,
		NarFile:      np.NarFile,
		Compression:  np.Compression,
		Refs:         nonNilStrings(np.References),
		Deriver:      np.Deriver,
		Ca:           np.CA,
		Signatures:   nonNilStrings(np.Signatures),
		BlobID:       blobID,
		CreatedAt:    createdAtOrNil(np.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("failed to create nix path: %w", err)
	}

	if err := q.DeleteNixPendingNar(ctx, dbsqlc.DeleteNixPendingNarParams{RepositoryID: np.RepositoryID, Sha256: np.SHA256}); err != nil {
		return fmt.Errorf("failed to drop nix pending nar: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit nix path: %w", err)
	}

	np.ID = row.ID
	np.CreatedAt = row.CreatedAt
	np.UpdatedAt = row.UpdatedAt

	return nil
}

// ListNixPaths returns every live path of the repository ordered by store hash.
func (p *PostgresDB) ListNixPaths(ctx context.Context, repoID string) ([]*models.NixPath, error) {
	rows, err := p.q.ListNixPaths(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list nix paths: %w", err)
	}

	paths := make([]*models.NixPath, 0, len(rows))
	for _, row := range rows {
		paths = append(paths, nixPathFromRow(dbsqlc.GetNixPathByHashRow(row)))
	}

	return paths, nil
}

// DeleteNixPath removes one path row.
func (p *PostgresDB) DeleteNixPath(ctx context.Context, repoID, id string) error {
	if err := p.q.DeleteNixPath(ctx, dbsqlc.DeleteNixPathParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to delete nix path: %w", err)
	}

	return nil
}

// IncrementNixDownloadCount counts one download of a path's NAR file.
func (p *PostgresDB) IncrementNixDownloadCount(ctx context.Context, id, repoID string) error {
	if err := p.q.IncrementNixDownloadCount(ctx, dbsqlc.IncrementNixDownloadCountParams{ID: id, RepositoryID: repoID}); err != nil {
		return fmt.Errorf("failed to count nix download: %w", err)
	}

	return nil
}

// DeleteStaleNixPendingNars deletes up to limit staging rows older than before
// and returns the sha256 of their NAR files so the caller can delete the blobs.
func (p *PostgresDB) DeleteStaleNixPendingNars(ctx context.Context, before time.Time, limit int) ([]string, error) {
	shas, err := p.q.DeleteStaleNixPendingNars(ctx, dbsqlc.DeleteStaleNixPendingNarsParams{
		Before: before, BatchLimit: int32(limit), //nolint:gosec // a small batch size
	})
	if err != nil {
		return nil, fmt.Errorf("failed to delete stale nix pending nars: %w", err)
	}

	return shas, nil
}

// GetNixSigningKey returns the repository's signing key, or (nil, nil).
func (p *PostgresDB) GetNixSigningKey(ctx context.Context, repoID string) (*models.NixSigningKey, error) {
	row, err := p.q.GetNixSigningKey(ctx, repoID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	case err != nil:
		return nil, fmt.Errorf("failed to get nix signing key: %w", err)
	}

	return &models.NixSigningKey{
		RepositoryID:  row.RepositoryID,
		KeyName:       row.KeyName,
		PublicKey:     row.PublicKey,
		PrivateKeyEnc: row.PrivateKey,
		CreatedBy:     row.CreatedBy,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// CreateNixSigningKeyIfAbsent stores the key unless the repository has one;
// created reports whether it did.
func (p *PostgresDB) CreateNixSigningKeyIfAbsent(ctx context.Context, k *models.NixSigningKey) (bool, error) {
	n, err := p.q.CreateNixSigningKeyIfAbsent(ctx, dbsqlc.CreateNixSigningKeyIfAbsentParams{
		RepositoryID: k.RepositoryID, KeyName: k.KeyName, PublicKey: k.PublicKey, PrivateKey: k.PrivateKeyEnc, CreatedBy: k.CreatedBy,
	})
	if err != nil {
		return false, fmt.Errorf("failed to create nix signing key: %w", err)
	}

	return n > 0, nil
}

// ReplaceNixSigningKey stores the key, replacing the repository's current one.
func (p *PostgresDB) ReplaceNixSigningKey(ctx context.Context, k *models.NixSigningKey) error {
	err := p.q.ReplaceNixSigningKey(ctx, dbsqlc.ReplaceNixSigningKeyParams{
		RepositoryID: k.RepositoryID, KeyName: k.KeyName, PublicKey: k.PublicKey, PrivateKey: k.PrivateKeyEnc, CreatedBy: k.CreatedBy,
	})
	if err != nil {
		return fmt.Errorf("failed to replace nix signing key: %w", err)
	}

	return nil
}
