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

// StorageBackendStore owns the configured storage backends.
type StorageBackendStore struct{ *core }

// CreateStorageBackend creates a new storage backend configuration.
func (p *StorageBackendStore) CreateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	return p.q.CreateStorageBackend(ctx, dbsqlc.CreateStorageBackendParams{
		ID:            backend.ID,
		Name:          backend.Name,
		Kind:          backend.Kind,
		Config:        backend.Config,
		Status:        backend.Status,
		StatusMessage: backend.StatusMessage,
		CreatedAt:     orNow(backend.CreatedAt),
		UpdatedAt:     orNow(backend.UpdatedAt),
	})
}

// ListStorageBackends lists all storage backends.
func (p *StorageBackendStore) ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error) {
	rows, err := p.q.ListStorageBackends(ctx)
	if err != nil {
		return nil, err
	}

	backends := make([]*models.StorageBackend, 0, len(rows))

	for _, row := range rows {
		backends = append(backends, &models.StorageBackend{
			ID:            row.ID,
			Name:          row.Name,
			Kind:          row.Kind,
			Config:        row.Config,
			Status:        row.Status,
			StatusMessage: row.StatusMessage,
			LastCheckedAt: row.LastCheckedAt,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
		})
	}

	return backends, nil
}

// GetStorageBackend gets a storage backend by name.
func (p *StorageBackendStore) GetStorageBackend(ctx context.Context, name string) (*models.StorageBackend, error) {
	row, err := p.q.GetStorageBackend(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, err
	}

	return &models.StorageBackend{
		ID:            row.ID,
		Name:          row.Name,
		Kind:          row.Kind,
		Config:        row.Config,
		Status:        row.Status,
		StatusMessage: row.StatusMessage,
		LastCheckedAt: row.LastCheckedAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

// UpdateStorageBackend updates a storage backend configuration.
func (p *StorageBackendStore) UpdateStorageBackend(ctx context.Context, backend *models.StorageBackend) error {
	return p.q.UpdateStorageBackend(ctx, dbsqlc.UpdateStorageBackendParams{
		Name:      backend.Name,
		Kind:      backend.Kind,
		Config:    backend.Config,
		UpdatedAt: backend.UpdatedAt,
		ID:        backend.ID,
	})
}

// DeleteStorageBackend deletes a storage backend.
func (p *StorageBackendStore) DeleteStorageBackend(ctx context.Context, id string) error {
	return p.q.DeleteStorageBackend(ctx, id)
}

// UpdateStorageBackendStatus updates the status of a storage backend.
func (p *StorageBackendStore) UpdateStorageBackendStatus(ctx context.Context, id, status string, message *string) error {
	return p.q.UpdateStorageBackendStatus(ctx, dbsqlc.UpdateStorageBackendStatusParams{
		Status:        status,
		StatusMessage: message,
		ID:            id,
	})
}

// CountBlobsOnStorageBackend returns how many blobs are recorded on a storage backend.
func (p *StorageBackendStore) CountBlobsOnStorageBackend(ctx context.Context, backendID string) (int64, error) {
	var n int64

	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blobs WHERE storage_backend_id = $1`, backendID).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count blobs on storage backend: %w", err)
	}

	return n, nil
}

// StorageBackendBusyReason reports why a backend's kind or config must not
// change (or the backend not be deleted): it holds blobs, is served by a tier
// or is named by a move journal row. "" means it is idle.
func (p *StorageBackendStore) StorageBackendBusyReason(ctx context.Context, backendID string) (string, error) {
	return storageBackendBusy(ctx, p.db, backendID, true)
}
