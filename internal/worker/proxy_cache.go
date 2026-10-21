// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// proxyCacheGrace keeps expired entries around briefly so a blob is never
// deleted while a concurrent request may still be re-registering it.
const proxyCacheGrace = "1 hour"

// evictExpiredSQL deletes entries expired beyond the grace period. A cached
// file (non-null blob_key) is additionally kept for its repository's
// max_stale_seconds, the window in which it may be served stale while the
// upstream is failing; negative entries are evicted as soon as grace ends.
const evictExpiredSQL = `
	DELETE FROM proxy_cache_entries e
	USING repositories r
	WHERE r.id = e.repository_id
	  AND r.deleted_at IS NULL
	  AND e.expires_at < now() - interval '` + proxyCacheGrace + `' - CASE
	      WHEN e.blob_key IS NOT NULL THEN COALESCE(r.max_stale_seconds, 0) * interval '1 second'
	      ELSE interval '0'
	  END
	RETURNING COALESCE(e.blob_key, '')`

type ProxyCacheWorker struct {
	db        database.Database
	blobStore storage.BlobStore
	log       *logger.Logger
	interval  time.Duration
	stopCh    chan struct{}
	doneCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	started   bool
	ctx       context.Context //nolint:containedctx // worker lifetime context, cancelled by Stop
	cancel    context.CancelFunc
}

// NewProxyCacheWorker creates the eviction worker. blobStore may be nil, in
// which case only database rows are evicted.
func NewProxyCacheWorker(db database.Database, blobStore storage.BlobStore, log *logger.Logger) *ProxyCacheWorker {
	ctx, cancel := context.WithCancel(context.Background())

	return &ProxyCacheWorker{
		db:        db,
		blobStore: blobStore,
		log:       log,
		interval:  5 * time.Minute,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (w *ProxyCacheWorker) Start() {
	w.startOnce.Do(func() {
		w.started = true

		go func() {
			defer close(w.doneCh)

			ticker := time.NewTicker(w.interval)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					w.evictSafely()
				case <-w.stopCh:
					return
				}
			}
		}()
	})
}

func (w *ProxyCacheWorker) evictSafely() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in proxy cache eviction", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
	defer cancel()

	n, err := w.evict(ctx)
	if err != nil {
		w.log.Errorw("proxy cache eviction failed", "error", err)
	} else if n > 0 {
		w.log.Infow("proxy cache eviction complete", "evicted", n)
	}
}

// evict deletes entries expired beyond the grace period and removes the blobs
// they referenced when no other cache entry still points at them.
func (w *ProxyCacheWorker) evict(ctx context.Context) (int, error) {
	sqlDB := w.db.GetDB()
	if sqlDB == nil || w.blobStore == nil {
		return int(w.deleteRowsOnly(ctx)), nil
	}

	keys, total, err := deleteExpiredEntries(ctx, sqlDB)
	if err != nil {
		return total, err
	}

	for k := range keys {
		var stillUsed bool
		if err := sqlDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM proxy_cache_entries WHERE blob_key = $1)`, k).Scan(&stillUsed); err != nil || stillUsed {
			continue
		}

		if err := storage.SafeDeleteBlob(ctx, w.db, w.blobStore, k); err != nil {
			w.log.Warnw("failed to delete evicted proxy blob", "blob_key", k, "error", err)
		}
	}

	return total, nil
}

// deleteExpiredEntries deletes entries expired beyond the grace period (and
// their repository's max-stale window) and
// returns the distinct blob keys they referenced plus the number of rows deleted.
func deleteExpiredEntries(ctx context.Context, sqlDB *sql.DB) (map[string]struct{}, int, error) {
	rows, err := sqlDB.QueryContext(ctx, evictExpiredSQL)
	if err != nil {
		return nil, 0, fmt.Errorf("delete expired proxy cache entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keys := map[string]struct{}{}
	total := 0

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return keys, total, fmt.Errorf("scan evicted entry: %w", err)
		}

		total++

		if k != "" {
			keys[k] = struct{}{}
		}
	}

	if err := rows.Err(); err != nil {
		return keys, total, err
	}

	return keys, total, nil
}

func (w *ProxyCacheWorker) deleteRowsOnly(ctx context.Context) int64 {
	n, err := w.db.DeleteExpiredProxyCacheEntries(ctx)
	if err != nil {
		w.log.Errorw("proxy cache eviction failed", "error", err)
	}

	return n
}

func (w *ProxyCacheWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })

	if !w.started {
		w.cancel()
		return
	}

	select {
	case <-w.doneCh:
	case <-time.After(10 * time.Second):
		w.cancel()
		<-w.doneCh
	}

	w.cancel()
}
