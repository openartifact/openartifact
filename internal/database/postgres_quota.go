// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
