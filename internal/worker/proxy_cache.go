// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// proxyCacheGrace keeps expired entries around briefly so a blob is never
// deleted while a concurrent request may still be re-registering it.
const proxyCacheGrace = "1 hour"

// evictBatchSize bounds one eviction statement, so a burst of expiring entries
// (a bulk TTL, a freshly warmed proxy) is deleted in short transactions that
// commit as they go instead of one long delete that the run's timeout rolls
// back as a whole.
const evictBatchSize = 2000

// evictExpiredSQL deletes up to $1 entries expired beyond the grace period,
// oldest first. A cached file (non-null blob_key) is additionally kept for its
// repository's max_stale_seconds, the window in which it may be served stale
// while the upstream is failing; negative entries are evicted as soon as grace
// ends.
//
// The first expires_at predicate is the sargable lower bound every deletable
// row satisfies (the CASE only ever subtracts more): it lets the planner walk
// idx_proxy_cache_expires from the oldest row instead of sequentially scanning
// the whole table to evaluate the per-repository expression. SKIP LOCKED leaves
// rows a concurrent upsert holds for the next run.
const evictExpiredSQL = `
	WITH victims AS (
		SELECT e.id
		FROM proxy_cache_entries e
		JOIN repositories r ON r.id = e.repository_id
		WHERE r.deleted_at IS NULL
		  AND e.expires_at < now() - interval '` + proxyCacheGrace + `'
		  AND e.expires_at < now() - interval '` + proxyCacheGrace + `' - CASE
		      WHEN e.blob_key IS NOT NULL THEN COALESCE(r.max_stale_seconds, 0) * interval '1 second'
		      ELSE interval '0'
		  END
		ORDER BY e.expires_at
		LIMIT $1
		FOR UPDATE OF e SKIP LOCKED
	)
	DELETE FROM proxy_cache_entries e
	USING victims v
	WHERE e.id = v.id
	RETURNING COALESCE(e.blob_key, '')`

// unreferencedCacheKeysSQL picks, from the blob keys of just-deleted entries,
// those no remaining cache entry points at (idx_proxy_cache_blob_key).
const unreferencedCacheKeysSQL = `
	SELECT k FROM unnest($1::text[]) AS k
	WHERE NOT EXISTS (SELECT 1 FROM proxy_cache_entries WHERE blob_key = k)`

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

	total := 0

	// Each batch commits on its own, so progress survives the run's timeout; a
	// blob key shared with an entry of a later batch is skipped here (that entry
	// still points at it) and collected again with that batch.
	for {
		deleted, keys, err := deleteExpiredBatch(ctx, sqlDB)
		total += deleted

		if err != nil {
			return total, err
		}

		w.deleteUnreferencedBlobs(ctx, sqlDB, keys)

		if deleted < evictBatchSize || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}

// deleteUnreferencedBlobs removes the stored blobs for keys that no cache entry
// references any more, with one lookup for the whole batch.
func (w *ProxyCacheWorker) deleteUnreferencedBlobs(ctx context.Context, sqlDB *sql.DB, keys []string) {
	if len(keys) == 0 {
		return
	}

	unused, err := unreferencedKeys(ctx, sqlDB, keys)
	if err != nil {
		w.log.Warnw("failed to check evicted proxy blobs", "error", err)
		return
	}

	for _, k := range unused {
		if err := storage.SafeDeleteBlob(ctx, w.db, w.blobStore, k); err != nil {
			w.log.Warnw("failed to delete evicted proxy blob", "blob_key", k, "error", err)
		}
	}
}

// unreferencedKeys returns the subset of keys no cache entry points at any
// more, in one round trip.
func unreferencedKeys(ctx context.Context, sqlDB *sql.DB, keys []string) ([]string, error) {
	rows, err := sqlDB.QueryContext(ctx, unreferencedCacheKeysSQL, keys)
	if err != nil {
		return nil, fmt.Errorf("query unreferenced cache keys: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var unused []string

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan unreferenced cache key: %w", err)
		}

		unused = append(unused, k)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read unreferenced cache keys: %w", err)
	}

	return unused, nil
}

// deleteExpiredBatch deletes up to evictBatchSize entries expired beyond the
// grace period (and their repository's max-stale window) and returns how many
// rows it deleted plus the distinct blob keys they referenced.
func deleteExpiredBatch(ctx context.Context, sqlDB *sql.DB) (int, []string, error) {
	rows, err := sqlDB.QueryContext(ctx, evictExpiredSQL, evictBatchSize)
	if err != nil {
		return 0, nil, fmt.Errorf("delete expired proxy cache entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keys := map[string]struct{}{}
	total := 0

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return total, nil, fmt.Errorf("scan evicted entry: %w", err)
		}

		total++

		if k != "" {
			keys[k] = struct{}{}
		}
	}

	if err := rows.Err(); err != nil {
		return total, nil, err
	}

	return total, slices.Collect(maps.Keys(keys)), nil
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
