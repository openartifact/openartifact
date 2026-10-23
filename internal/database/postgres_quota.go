// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
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

// reserveStorageLedgerSQL is reserveStorageSQL plus the quota_pending entry
// (kind 0) of the reservation, in the same statement: the reconcile counts the
// entry until the package row or the release consumes it, so a recount while
// the upload is in flight stays exact. It is for non-zero reservations only;
// the rows affected are 1 when the reservation fit and 0 when it did not.
const reserveStorageLedgerSQL = `
WITH reserved AS (` + reserveStorageSQL + `
RETURNING 1)
INSERT INTO quota_pending (repository_id, size_bytes, artifact_count)
SELECT $3::uuid, $1::bigint, $2::integer FROM reserved`

// takePendingSQL consumes the pending entries of a release (exact match
// first, then the oldest entries that fit, for batch releases).
const takePendingSQL = `SELECT quota_pending_take($3::uuid, $1::bigint, $2::integer, true)`

// reserveSQL picks the reservation statement: a reservation of nothing leaves
// no pending entry.
func reserveSQL(sizeBytes int64, artifactCount int) string {
	if sizeBytes == 0 && artifactCount == 0 {
		return reserveStorageSQL
	}

	return reserveStorageLedgerSQL
}

// releaseStorageSQL returns ($1 bytes, $2 artifacts) to the repository ($3),
// never taking a counter below zero.
const releaseStorageSQL = `
UPDATE storage_quotas
   SET current_size_bytes = GREATEST(0, current_size_bytes - $1),
       current_artifact_count = GREATEST(0, current_artifact_count - $2)
 WHERE repository_id = $3`

// reserveProjectSQL adds ($1 bytes, $2 artifacts) to the project ($3) ceiling
// unless that would pass a non-zero limit. Zero rows affected means the project
// quota would be exceeded.
const reserveProjectSQL = `
UPDATE project_quotas
   SET current_size_bytes = current_size_bytes + $1,
       current_artifact_count = current_artifact_count + $2
 WHERE project_id = $3
   AND (max_size_bytes = 0 OR current_size_bytes + $1 <= max_size_bytes)
   AND (max_artifact_count = 0 OR current_artifact_count + $2 <= max_artifact_count)`

// releaseProjectSQL returns ($1 bytes, $2 artifacts) of repository $3 to its
// project's ceiling. Only a project with a limit keeps counters; an unlimited
// project (or one without a quota row) matches nothing and costs nothing.
const releaseProjectSQL = `
UPDATE project_quotas
   SET current_size_bytes = GREATEST(0, current_size_bytes - $1),
       current_artifact_count = GREATEST(0, current_artifact_count - $2)
 WHERE project_id = (SELECT project_id FROM repositories WHERE id = $3)
   AND (max_size_bytes > 0 OR max_artifact_count > 0)`

// limitedProjectSQL finds the project of repository $1 when that project has a
// ceiling to enforce.
const limitedProjectSQL = `
SELECT pq.project_id
  FROM repositories r
  JOIN project_quotas pq ON pq.project_id = r.project_id
 WHERE r.id = $1 AND (pq.max_size_bytes > 0 OR pq.max_artifact_count > 0)`

// Lock order. Quota counters live on two levels, the project ceiling
// (project_quotas) and the repository (storage_quotas). Every code path that
// touches both takes the project row first and the repository row second:
// reserve (below), release (one batch, project statement first) and transfer
// (both projects in id order, then the repository). Paths touching one level
// only cannot close a cycle. Reconcile rewrites the repository counters in
// one transaction and the project counters in a later one, never holding both.

// execQuota runs the quota counter statements (all taking bytes, artifacts and
// the repository) in one short transaction with asynchronous commit (see above)
// and returns the rows the last one affected. The setting and the statements
// travel in one batch, i.e. one round trip; the batch is a single implicit
// transaction, so either every statement applies or none does.
func (p *PostgresDB) execQuota(ctx context.Context, sizeBytes int64, artifactCount int, repoID string, statements ...string) (int64, error) {
	batch := &pgx.Batch{}
	batch.Queue(`SELECT set_config('synchronous_commit', 'off', true)`)

	for _, st := range statements {
		batch.Queue(st, sizeBytes, int64(artifactCount), repoID)
	}

	br := p.pool.SendBatch(ctx, batch)

	if _, err := br.Exec(); err != nil {
		_ = br.Close()

		return 0, fmt.Errorf("set quota commit mode: %w", err)
	}

	var affected int64

	for range statements {
		tag, err := br.Exec()
		if err != nil {
			_ = br.Close()

			return 0, err
		}

		affected = tag.RowsAffected()
	}

	if err := br.Close(); err != nil {
		return 0, fmt.Errorf("commit quota update: %w", err)
	}

	return affected, nil
}

// ReserveStorageUsage atomically adds sizeBytes/artifactCount to the repository
// quota counters if doing so would not exceed the configured limits (a limit of
// 0 means unlimited), and, when the repository's project has a ceiling, to the
// project's counters too: the upload must fit both levels, otherwise nothing is
// reserved. It returns false, nil when a quota would be exceeded.
func (p *PostgresDB) ReserveStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) (bool, error) {
	var projectID string

	err := p.pool.QueryRow(ctx, limitedProjectSQL, repoID).Scan(&projectID)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No project ceiling: the repository statement alone (single round trip).
		n, err := p.execQuota(ctx, sizeBytes, artifactCount, repoID, reserveSQL(sizeBytes, artifactCount))
		if err != nil {
			return false, fmt.Errorf("failed to reserve storage usage: %w", err)
		}

		return n > 0, nil
	case err != nil:
		return false, fmt.Errorf("failed to look up project quota: %w", err)
	}

	return p.reserveTwoLevel(ctx, projectID, repoID, sizeBytes, artifactCount)
}

// reserveTwoLevel reserves on the project row, then the repository row, in one
// transaction (lock order: project, repository). A failed repository
// reservation rolls the project reservation back.
func (p *PostgresDB) reserveTwoLevel(ctx context.Context, projectID, repoID string, sizeBytes int64, artifactCount int) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to begin quota reservation: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT set_config('synchronous_commit', 'off', true)`); err != nil {
		return false, fmt.Errorf("set quota commit mode: %w", err)
	}

	tag, err := tx.Exec(ctx, reserveProjectSQL, sizeBytes, int64(artifactCount), projectID)
	if err != nil {
		return false, fmt.Errorf("failed to reserve project storage usage: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return false, nil
	}

	tag, err = tx.Exec(ctx, reserveSQL(sizeBytes, artifactCount), sizeBytes, int64(artifactCount), repoID)
	if err != nil {
		return false, fmt.Errorf("failed to reserve storage usage: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return false, nil // deferred rollback gives the project reservation back
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("failed to commit quota reservation: %w", err)
	}

	return true, nil
}

// chargeStorageSQL and chargeProjectSQL add ($1 bytes, $2 artifacts) to the
// repository ($3) and its project's ceiling without checking a limit and
// without a pending entry.
const (
	chargeStorageSQL = `
INSERT INTO storage_quotas AS q (repository_id, current_size_bytes, current_artifact_count)
VALUES ($3, $1, $2)
ON CONFLICT (repository_id) DO UPDATE
   SET current_size_bytes = q.current_size_bytes + EXCLUDED.current_size_bytes,
       current_artifact_count = q.current_artifact_count + EXCLUDED.current_artifact_count`

	chargeProjectSQL = `
UPDATE project_quotas
   SET current_size_bytes = current_size_bytes + $1,
       current_artifact_count = current_artifact_count + $2
 WHERE project_id = (SELECT project_id FROM repositories WHERE id = $3)
   AND (max_size_bytes > 0 OR max_artifact_count > 0)`
)

// ChargeStorageUsage adds usage whose package row already exists (a row that
// was created without a reservation), past any limit: the artifact is stored,
// so refusing it would only desynchronise the counters. Nothing is left
// pending.
func (p *PostgresDB) ChargeStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	if _, err := p.execQuota(ctx, sizeBytes, artifactCount, repoID, chargeProjectSQL, chargeStorageSQL); err != nil {
		return fmt.Errorf("failed to charge storage usage: %w", err)
	}

	return nil
}

// DecrementStorageUsage gives a reservation back (or accounts for a delete), on
// the repository and on its project's ceiling when that has one. A repository
// without a quota row has nothing to decrement.
func (p *PostgresDB) DecrementStorageUsage(ctx context.Context, repoID string, sizeBytes int64, artifactCount int) error {
	if _, err := p.execQuota(ctx, sizeBytes, artifactCount, repoID, releaseProjectSQL, releaseStorageSQL, takePendingSQL); err != nil {
		return fmt.Errorf("failed to decrement storage usage: %w", err)
	}

	return nil
}

// quotaUsageRowsSQL lists one row per stored package of every hosted format
// (the handlers that call quota.Enforce on upload) with the blob it points at
// and the number of artifacts it stands for (n). A repository's usage is the
// sum of those rows' blob sizes and of n. Blobs are deduplicated across rows
// and repositories, but a reservation is made per row at the row's full size,
// so the sum deliberately counts a shared blob once per row.
//
// A Go module is one artifact with two blobs (zip and go.mod): the zip row
// carries n = 1 and the go.mod row n = 0. Docker counts a manifest as one
// artifact and every layer/config blob the repository holds (docker_blobs) as
// bytes only (n = 0), so an image with many layers is one artifact. Proxy
// caches are not metered: they have no rows in these tables.
//
// Soft-deleted rows (deleted_at set) are intentionally NOT filtered out: a
// trashed package keeps its blob and its quota usage until it is purged, and
// only the purge releases the quota. Filtering them here would make the
// periodic reconcile hand back the usage of every item sitting in the trash.
const quotaUsageRowsSQL = `
SELECT repository_id, blob_id, 1 AS n FROM deb_packages
UNION ALL SELECT repository_id, blob_id, 1 FROM rpm_packages
UNION ALL SELECT repository_id, blob_id, 1 FROM maven_artifacts
UNION ALL SELECT repository_id, blob_id, 1 FROM helm_charts
UNION ALL SELECT repository_id, blob_id, 1 FROM rubygems_gems
UNION ALL SELECT p.repository_id, v.blob_id, 1 FROM npm_versions v JOIN npm_packages p ON p.id = v.package_id
UNION ALL SELECT repository_id, blob_id, 1 FROM pypi_packages
UNION ALL SELECT repository_id, blob_id, 1 FROM cargo_crates
UNION ALL SELECT repository_id, blob_id, 1 FROM nuget_packages
UNION ALL SELECT repository_id, blob_id, 1 FROM conda_packages
UNION ALL SELECT repository_id, blob_id, 1 FROM conan_packages
UNION ALL SELECT repository_id, blob_id, 1 FROM ivy_artifacts
UNION ALL SELECT repository_id, blob_id, 1 FROM sbt_artifacts
UNION ALL SELECT repository_id, blob_id, 1 FROM raw_artifacts
UNION ALL SELECT repository_id, zip_blob_id, 1 FROM gomod_modules
UNION ALL SELECT repository_id, mod_blob_id, 0 FROM gomod_modules
UNION ALL SELECT repository_id, blob_id, 1 FROM docker_manifests
UNION ALL SELECT repository_id, blob_id, 0 FROM docker_blobs`

// The reconcile statements rewrite the usage counters to the values computed
// from the rows and return how many counters changed. Repositories with rows
// get their quota row created when it is missing (unlimited, like a first
// reservation would); repositories whose rows are all gone are reset to zero.
// They are assembled from the pieces below so the single-repository variant
// can add its filter to both the row list and the reset.
const (
	// reconcileUsageSQL counts the rows and adds the pending entries that are
	// still fresh (see migration 061): a reservation whose row is not inserted
	// yet, or a deleted row whose usage is not returned yet, is part of the
	// counter and so of the recount. snap is the counter as of the statement's
	// snapshot; the fix applies only the difference to the counter's current
	// value, so a reservation committed after the snapshot is never lost.
	reconcileUsageSQL = `
WITH counted AS (
    SELECT r.repository_id,
           COALESCE(SUM(b.size_bytes), 0)::bigint AS size_bytes,
           COALESCE(SUM(r.n), 0)::bigint AS artifacts
      FROM (` + quotaUsageRowsSQL + `) r
      LEFT JOIN blobs b ON b.id = r.blob_id`

	reconcilePendingSQL = `
     GROUP BY r.repository_id
), pending AS (
    SELECT repository_id,
           COALESCE(SUM(size_bytes), 0)::bigint AS size_bytes,
           COALESCE(SUM(artifact_count), 0)::bigint AS artifacts
      FROM quota_pending
     WHERE created_at > now() - CASE kind WHEN 0 THEN interval '` + quotaPendingReserveTTL + `' ELSE interval '` + quotaPendingReleaseTTL + `' END`

	reconcilePendingEndSQL = `
     GROUP BY repository_id
), usage AS (
    SELECT COALESCE(c.repository_id, p.repository_id) AS repository_id,
           COALESCE(c.size_bytes, 0) + COALESCE(p.size_bytes, 0) AS size_bytes,
           COALESCE(c.artifacts, 0) + COALESCE(p.artifacts, 0) AS artifacts
      FROM counted c
      FULL JOIN pending p ON p.repository_id = c.repository_id
), snap AS (
    SELECT repository_id, current_size_bytes AS size_bytes, current_artifact_count AS artifacts
      FROM storage_quotas`

	reconcileFixSQL = `
), fixed AS (
    INSERT INTO storage_quotas AS q (repository_id, current_size_bytes, current_artifact_count)
    SELECT u.repository_id, u.size_bytes, u.artifacts FROM usage u
     WHERE EXISTS (SELECT 1 FROM repositories rp WHERE rp.id = u.repository_id)
    ON CONFLICT (repository_id) DO UPDATE
       SET current_size_bytes = GREATEST(0, q.current_size_bytes + EXCLUDED.current_size_bytes
               - COALESCE((SELECT s.size_bytes FROM snap s WHERE s.repository_id = q.repository_id), 0)),
           current_artifact_count = GREATEST(0, q.current_artifact_count + EXCLUDED.current_artifact_count
               - COALESCE((SELECT s.artifacts FROM snap s WHERE s.repository_id = q.repository_id), 0))
     WHERE COALESCE((SELECT s.size_bytes FROM snap s WHERE s.repository_id = q.repository_id), 0) <> EXCLUDED.current_size_bytes
        OR COALESCE((SELECT s.artifacts FROM snap s WHERE s.repository_id = q.repository_id), 0) <> EXCLUDED.current_artifact_count
    RETURNING 1
), emptied AS (
    UPDATE storage_quotas q
       SET current_size_bytes = GREATEST(0, q.current_size_bytes - s.size_bytes),
           current_artifact_count = GREATEST(0, q.current_artifact_count - s.artifacts)
      FROM snap s
     WHERE s.repository_id = q.repository_id
       AND (s.size_bytes <> 0 OR s.artifacts <> 0)
       AND NOT EXISTS (SELECT 1 FROM usage u WHERE u.repository_id = q.repository_id)`

	reconcileCountSQL = `
    RETURNING 1
)
SELECT (SELECT COUNT(*) FROM fixed) + (SELECT COUNT(*) FROM emptied)`

	// reconcileAllStorageSQL covers every repository.
	reconcileAllStorageSQL = reconcileUsageSQL + reconcilePendingSQL + reconcilePendingEndSQL + reconcileFixSQL + reconcileCountSQL

	// reconcileOneStorageSQL covers only the repository passed as $1.
	reconcileOneStorageSQL = reconcileUsageSQL + ` WHERE r.repository_id = $1::uuid` +
		reconcilePendingSQL + ` AND repository_id = $1::uuid` + reconcilePendingEndSQL +
		` WHERE repository_id = $1::uuid` + reconcileFixSQL + `
       AND q.repository_id = $1::uuid` + reconcileCountSQL
)

// Pending entries (quota_pending, migration 061) older than these were never
// matched by their partner (a crashed upload; a delete that returned no
// quota) and stop counting. A reservation lives as long as an upload can take;
// a delete returns its quota right after the row is gone.
const (
	quotaPendingReserveTTL = "1 hour"
	quotaPendingReleaseTTL = "2 minutes"
)

// purgeStalePendingSQL drops the pending entries that expired (or whose
// repository is gone) so the table does not keep what no partner will consume.
const purgeStalePendingSQL = `
DELETE FROM quota_pending
 WHERE created_at <= now() - CASE kind WHEN 0 THEN interval '` + quotaPendingReserveTTL + `' ELSE interval '` + quotaPendingReleaseTTL + `' END
    OR NOT EXISTS (SELECT 1 FROM repositories r WHERE r.id = quota_pending.repository_id)`

// reconcileProjectsSQL rewrites the counters of every project with a ceiling
// from the (already reconciled) repository counters; $1, when not NULL,
// restricts it to that project. It returns the number of counters changed.
// Run in its own transaction after the repository counters are committed so it
// never holds a project row while waiting on repository rows (lock order).
const reconcileProjectsSQL = `
WITH sums AS (
    SELECT r.project_id,
           COALESCE(SUM(sq.current_size_bytes), 0)::bigint AS size_bytes,
           COALESCE(SUM(sq.current_artifact_count), 0)::bigint AS artifacts
      FROM repositories r
      LEFT JOIN storage_quotas sq ON sq.repository_id = r.id
     WHERE $1::uuid IS NULL OR r.project_id = $1::uuid
     GROUP BY r.project_id
), fixed AS (
    UPDATE project_quotas pq
       SET current_size_bytes = s.size_bytes,
           current_artifact_count = s.artifacts
      FROM sums s
     WHERE s.project_id = pq.project_id
       AND (pq.max_size_bytes > 0 OR pq.max_artifact_count > 0)
       AND (pq.current_size_bytes <> s.size_bytes OR pq.current_artifact_count <> s.artifacts)
    RETURNING 1
)
SELECT COUNT(*) FROM fixed`

// reconcileProjects runs reconcileProjectsSQL (projectID "" = every project).
func (p *PostgresDB) reconcileProjects(ctx context.Context, projectID string) (int64, error) {
	var arg *string
	if projectID != "" {
		arg = &projectID
	}

	var changed int64
	if err := p.pool.QueryRow(ctx, reconcileProjectsSQL, arg).Scan(&changed); err != nil {
		return 0, fmt.Errorf("failed to reconcile project storage usage: %w", err)
	}

	return changed, nil
}

// quotaReconcileLockKey serializes ReconcileAllStorageUsage across replicas.
const quotaReconcileLockKey = `hashtext('openartifact-quota-reconcile')::bigint`

// ReconcileStorageUsage recomputes the usage counters of one repository from
// its package rows and returns the corrected quota (creating the quota row,
// unlimited, if it did not exist). The counters are maintained incrementally
// with asynchronous commits, so a crash can leave them off; this is the repair.
//
// The recount is one statement over a single snapshot. An upload that has
// reserved but not yet inserted its row, or a delete that removed the row but
// not yet released it, is accounted for by its quota_pending entry, and the fix
// adds only the difference to the counter's current value, so the result is
// exact at every instant (entries older than quotaPendingReserveTTL and
// quotaPendingReleaseTTL are abandoned operations and do not count).
func (p *PostgresDB) ReconcileStorageUsage(ctx context.Context, repoID string) (*models.StorageQuota, error) {
	if _, err := p.pool.Exec(ctx, purgeStalePendingSQL); err != nil {
		return nil, fmt.Errorf("failed to purge stale pending quota entries: %w", err)
	}

	if _, err := p.pool.Exec(ctx, reconcileOneStorageSQL, repoID); err != nil {
		return nil, fmt.Errorf("failed to reconcile storage usage: %w", err)
	}

	var projectID string
	if err := p.pool.QueryRow(ctx, `SELECT COALESCE(project_id::text, '') FROM repositories WHERE id = $1`, repoID).Scan(&projectID); err == nil && projectID != "" {
		if _, err := p.reconcileProjects(ctx, projectID); err != nil {
			return nil, err
		}
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

	if _, err := tx.Exec(ctx, purgeStalePendingSQL); err != nil {
		return 0, fmt.Errorf("failed to purge stale pending quota entries: %w", err)
	}

	var changed int64
	if err := tx.QueryRow(ctx, reconcileAllStorageSQL).Scan(&changed); err != nil {
		return 0, fmt.Errorf("failed to reconcile storage usage: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit storage reconcile: %w", err)
	}

	projects, err := p.reconcileProjects(ctx, "")
	if err != nil {
		return changed, err
	}

	return changed + projects, nil
}
