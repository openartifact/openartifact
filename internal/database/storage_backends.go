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

// UpdateStorageBackendIfIdle updates a storage backend's name, kind and config.
// When requireIdle is set (the kind or location changes), the backend row is
// locked FOR UPDATE and the busy check runs in the same transaction, so a
// concurrent tier move or blob write (which takes a key-share lock on the row)
// either commits first and is seen, or waits until the update is done. It
// returns "" when the backend was updated and the busy reason otherwise.
func (p *StorageBackendStore) UpdateStorageBackendIfIdle(
	ctx context.Context,
	backend *models.StorageBackend,
	requireIdle bool,
) (string, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin update storage backend tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if requireIdle {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM storage_backends WHERE id = $1 FOR UPDATE`, backend.ID); err != nil {
			return "", fmt.Errorf("lock storage backend: %w", err)
		}

		if reason, err := storageBackendBusy(ctx, tx, backend.ID, true); err != nil || reason != "" {
			return reason, err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE storage_backends SET name = $1, kind = $2, config = $3, updated_at = $4 WHERE id = $5`,
		backend.Name, backend.Kind, backend.Config, backend.UpdatedAt, backend.ID); err != nil {
		return "", fmt.Errorf("update storage backend: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit update storage backend: %w", err)
	}

	return "", nil
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

// StorageBackendBusyReason reports why a backend's kind or config must not
// change (or the backend not be deleted): it holds blobs, is served by a tier
// or is named by a move journal row. "" means it is idle.
func (p *StorageBackendStore) StorageBackendBusyReason(ctx context.Context, backendID string) (string, error) {
	return storageBackendBusy(ctx, p.db, backendID, true)
}
