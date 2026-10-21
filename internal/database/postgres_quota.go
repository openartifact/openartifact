// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Every upload reserves quota on its repository's single storage_quotas row and
// every delete or failed upload gives it back, so that row is the hottest in the
// schema. A row lock is held until the updating transaction's commit record is
// durable, which makes a synchronous commit (one WAL fsync, ~ms) the critical
// section: concurrent uploads to one repository then queue behind each other's
// fsync (measured ~120 ms mean at 32 writers). The counter statements below
// therefore run in a transaction with synchronous_commit off, which releases
// the lock as soon as the commit is in the WAL buffer (microseconds). The price
// is that a database crash can lose the counter changes of the last few hundred
// milliseconds; the artifact rows themselves keep their normal durability. The
// reservation is a single statement (insert-or-conditional-update), so
// check-and-update stays atomic across replicas without any advisory lock.

// reserveStorageSQL adds ($1 bytes, $2 artifacts) to the repository's usage
// ($3) unless that would pass a non-zero limit. A missing quota row is created
// (unlimited, with the usage as its initial value) in the same statement; a
// repository that does not exist fails the foreign key. Zero rows affected
// means the quota would be exceeded.
const reserveStorageSQL = `
INSERT INTO storage_quotas AS q (repository_id, current_size_bytes, current_artifact_count)
VALUES ($3, $1, $2)
ON CONFLICT (repository_id) DO UPDATE
   SET current_size_bytes = q.current_size_bytes + EXCLUDED.current_size_bytes,
       current_artifact_count = q.current_artifact_count + EXCLUDED.current_artifact_count
 WHERE (q.max_size_bytes = 0 OR q.current_size_bytes + EXCLUDED.current_size_bytes <= q.max_size_bytes)
   AND (q.max_artifact_count = 0 OR q.current_artifact_count + EXCLUDED.current_artifact_count <= q.max_artifact_count)`

// releaseStorageSQL returns ($1 bytes, $2 artifacts) to the repository ($3),
// never taking a counter below zero.
const releaseStorageSQL = `
UPDATE storage_quotas
   SET current_size_bytes = GREATEST(0, current_size_bytes - $1),
       current_artifact_count = GREATEST(0, current_artifact_count - $2)
 WHERE repository_id = $3`

// execQuota runs one quota counter statement in its own short transaction with
// asynchronous commit (see above) and returns the rows it affected. The setting
// and the statement travel in one batch, i.e. one round trip.
func (p *PostgresDB) execQuota(ctx context.Context, sql string, sizeBytes int64, artifactCount int, repoID string) (int64, error) {
	batch := &pgx.Batch{}
	batch.Queue(`SELECT set_config('synchronous_commit', 'off', true)`)
	batch.Queue(sql, sizeBytes, int64(artifactCount), repoID)

	br := p.pool.SendBatch(ctx, batch)

	if _, err := br.Exec(); err != nil {
		_ = br.Close()

		return 0, fmt.Errorf("set quota commit mode: %w", err)
	}

	tag, err := br.Exec()
	if err != nil {
		_ = br.Close()

		return 0, err
	}

	if err := br.Close(); err != nil {
		return 0, fmt.Errorf("commit quota update: %w", err)
	}

	return tag.RowsAffected(), nil
}

// ReserveStorageUsage atomically adds sizeBytes/artifactCount to the repository
// quota counters if doing so would not exceed the configured limits (a limit of
// 0 means unlimited). It returns false, nil when the quota would be exceeded.
func (p *PostgresDB) ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error) {
	n, err := p.execQuota(ctx, reserveStorageSQL, sizeBytes, artifactCount, repoID)
	if err != nil {
		return false, fmt.Errorf("failed to reserve storage usage: %w", err)
	}

	return n > 0, nil
}

// DecrementStorageUsage gives a reservation back (or accounts for a delete). A
// repository without a quota row has nothing to decrement.
func (p *PostgresDB) DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	if _, err := p.execQuota(ctx, releaseStorageSQL, sizeBytes, artifactCount, repoID); err != nil {
		return fmt.Errorf("failed to decrement storage usage: %w", err)
	}

	return nil
}

// quotaUsageRowsSQL lists one row per stored package of every quota-enforced
// format (the handlers that call Enforce on upload: deb, rpm, maven, helm,
// npm, rubygems) with the blob it points at. A repository's usage is the sum of
// those rows' blob sizes and their number. Blobs are deduplicated across rows
// and repositories, but a reservation is made per row at the row's full size,
// so the sum deliberately counts a shared blob once per row.
const quotaUsageRowsSQL = `
SELECT repository_id, blob_id FROM deb_packages
UNION ALL SELECT repository_id, blob_id FROM rpm_packages
UNION ALL SELECT repository_id, blob_id FROM maven_artifacts
UNION ALL SELECT repository_id, blob_id FROM helm_charts
UNION ALL SELECT repository_id, blob_id FROM rubygems_gems
UNION ALL SELECT p.repository_id, v.blob_id FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id`

// The reconcile statements rewrite the usage counters to the values computed
// from the rows and return how many counters changed. Repositories with rows
// get their quota row created when it is missing (unlimited, like a first
// reservation would); repositories whose rows are all gone are reset to zero.
// They are assembled from the pieces below so the single-repository variant
// can add its filter to both the row list and the reset.
const (
	reconcileUsageSQL = `
WITH usage AS (
    SELECT r.repository_id,
           COALESCE(SUM(b.size_bytes), 0)::bigint AS size_bytes,
           COUNT(*)::bigint AS artifacts
      FROM (` + quotaUsageRowsSQL + `) r
      LEFT JOIN blobs b ON b.id = r.blob_id`

	reconcileFixSQL = `
     GROUP BY r.repository_id
), fixed AS (
    INSERT INTO storage_quotas AS q (repository_id, current_size_bytes, current_artifact_count)
    SELECT repository_id, size_bytes, artifacts FROM usage
    ON CONFLICT (repository_id) DO UPDATE
       SET current_size_bytes = EXCLUDED.current_size_bytes,
           current_artifact_count = EXCLUDED.current_artifact_count
     WHERE q.current_size_bytes <> EXCLUDED.current_size_bytes
        OR q.current_artifact_count <> EXCLUDED.current_artifact_count
    RETURNING 1
), emptied AS (
    UPDATE storage_quotas q
       SET current_size_bytes = 0, current_artifact_count = 0
     WHERE (q.current_size_bytes <> 0 OR q.current_artifact_count <> 0)
       AND NOT EXISTS (SELECT 1 FROM usage u WHERE u.repository_id = q.repository_id)`

	reconcileCountSQL = `
    RETURNING 1
)
SELECT (SELECT COUNT(*) FROM fixed) + (SELECT COUNT(*) FROM emptied)`

	// reconcileAllStorageSQL covers every repository.
	reconcileAllStorageSQL = reconcileUsageSQL + reconcileFixSQL + reconcileCountSQL

	// reconcileOneStorageSQL covers only the repository passed as $1.
	reconcileOneStorageSQL = reconcileUsageSQL +
		` WHERE r.repository_id = $1::uuid` + reconcileFixSQL +
		` AND q.repository_id = $1::uuid` + reconcileCountSQL
)

// quotaReconcileLockKey serializes ReconcileAllStorageUsage across replicas.
const quotaReconcileLockKey = `hashtext('openartifact-quota-reconcile')::bigint`

// ReconcileStorageUsage recomputes the usage counters of one repository from
// its package rows and returns the corrected quota (creating the quota row,
// unlimited, if it did not exist). The counters are maintained incrementally
// with asynchronous commits, so a crash can leave them off; this is the repair.
//
// The recount is one statement over a single snapshot: an upload that has
// reserved but not yet inserted its row (or a delete that removed the row but
// not yet released) is not seen consistently, so the result can be off by those
// in-flight operations until the next reconcile.
func (p *PostgresDB) ReconcileStorageUsage(ctx context.Context, repoID string) (*models.StorageQuota, error) {
	if _, err := p.pool.Exec(ctx, reconcileOneStorageSQL, repoID); err != nil {
		return nil, fmt.Errorf("failed to reconcile storage usage: %w", err)
	}

	return p.GetOrCreateStorageQuota(ctx, repoID)
}

// ReconcileAllStorageUsage reconciles every repository (see
// ReconcileStorageUsage) and returns how many counters were corrected. Only one
// run is active at a time across replicas: when another holds the lock this
// returns 0, nil immediately.
func (p *PostgresDB) ReconcileAllStorageUsage(ctx context.Context) (int64, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin storage reconcile: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(`+quotaReconcileLockKey+`)`).Scan(&locked); err != nil {
		return 0, fmt.Errorf("failed to lock storage reconcile: %w", err)
	}

	if !locked {
		return 0, nil
	}

	var changed int64
	if err := tx.QueryRow(ctx, reconcileAllStorageSQL).Scan(&changed); err != nil {
		return 0, fmt.Errorf("failed to reconcile storage usage: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit storage reconcile: %w", err)
	}

	return changed, nil
}
