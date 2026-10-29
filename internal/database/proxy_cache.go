// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// ProxyCacheStore owns the proxy cache entries.
type ProxyCacheStore struct{ *core }

func (p *ProxyCacheStore) GetProxyCacheEntry(ctx context.Context, repoID, cacheKey string) (*models.ProxyCacheEntry, error) {
	row, err := p.q.GetProxyCacheEntry(ctx, dbsqlc.GetProxyCacheEntryParams{
		RepositoryID: repoID,
		CacheKey:     cacheKey,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get proxy cache entry: %w", err)
	}

	return &models.ProxyCacheEntry{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		CacheKey:     row.CacheKey,
		BlobKey:      row.BlobKey,
		ContentType:  row.ContentType,
		StatusCode:   int(row.StatusCode),
		SizeBytes:    row.SizeBytes,
		HitCount:     int(row.HitCount),
		LastHitAt:    row.LastHitAt,
		ExpiresAt:    row.ExpiresAt,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// maxNegativeProxyEntriesPerRepo bounds the live 404 rows one proxy repository
// may hold; beyond it new missing paths are simply not negative-cached.
const maxNegativeProxyEntriesPerRepo = 10000

func (p *ProxyCacheStore) UpsertProxyCacheEntry(ctx context.Context, entry *models.ProxyCacheEntry) error {
	if entry.StatusCode == http.StatusNotFound && entry.BlobKey == "" {
		if err := p.q.UpsertNegativeProxyCacheEntry(ctx, dbsqlc.UpsertNegativeProxyCacheEntryParams{
			RepositoryID: entry.RepositoryID,
			CacheKey:     entry.CacheKey,
			ExpiresAt:    entry.ExpiresAt,
			Limit:        maxNegativeProxyEntriesPerRepo,
		}); err != nil {
			return fmt.Errorf("failed to upsert negative proxy cache entry: %w", err)
		}

		return nil
	}

	row, err := p.q.UpsertProxyCacheEntry(ctx, dbsqlc.UpsertProxyCacheEntryParams{
		RepositoryID: entry.RepositoryID,
		CacheKey:     entry.CacheKey,
		Column3:      entry.BlobKey,
		Column4:      entry.ContentType,
		StatusCode:   int32(entry.StatusCode), //nolint:gosec // G115: status code is bounded
		SizeBytes:    entry.SizeBytes,
		HitCount:     int64(entry.HitCount),
		LastHitAt:    entry.LastHitAt,
		ExpiresAt:    entry.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert proxy cache entry: %w", err)
	}

	entry.ID = row.ID
	entry.CreatedAt = row.CreatedAt
	entry.UpdatedAt = row.UpdatedAt

	return nil
}

func (p *ProxyCacheStore) DeleteProxyCacheEntry(ctx context.Context, repoID, cacheKey string) error {
	if err := p.q.DeleteProxyCacheEntry(ctx, dbsqlc.DeleteProxyCacheEntryParams{
		RepositoryID: repoID,
		CacheKey:     cacheKey,
	}); err != nil {
		return fmt.Errorf("failed to delete proxy cache entry: %w", err)
	}

	return nil
}

func (p *ProxyCacheStore) DeleteExpiredProxyCacheEntries(ctx context.Context) (int64, error) {
	n, err := p.q.DeleteExpiredProxyCacheEntries(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired proxy cache entries: %w", err)
	}

	return n, nil
}

// ListProxyCacheKeys implements OSVStore.
func (p *ProxyCacheStore) ListProxyCacheKeys(ctx context.Context, format, afterID string, limit int) ([]models.ProxyCacheKey, error) {
	var after *string
	if afterID != "" {
		after = &afterID
	}

	rows, err := p.db.QueryContext(ctx, `
		SELECT t.id::text, t.repository_id::text, t.cache_key, t.created_at
		FROM proxy_cache_entries t
		JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL AND r.format = $1
		WHERE t.status_code = 200 AND t.blob_key IS NOT NULL AND ($2::uuid IS NULL OR t.id > $2::uuid)
		ORDER BY t.id
		LIMIT $3`, format, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list proxy cache keys: %w", err)
	}
	defer rows.Close()

	var keys []models.ProxyCacheKey

	for rows.Next() {
		var k models.ProxyCacheKey
		if err := rows.Scan(&k.ID, &k.RepositoryID, &k.CacheKey, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan proxy cache key: %w", err)
		}

		keys = append(keys, k)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list proxy cache keys: %w", err)
	}

	return keys, nil
}
