// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

var _ BlobDeletionStore = (*PostgresDB)(nil)

// enqueueBlobDeletionSQL is the statement behind sqlc's EnqueueBlobDeletion,
// run inside the transaction that removes the blobs row (sqlc's pgx queries
// cannot join a database/sql transaction). Keep the two in sync.
const enqueueBlobDeletionSQL = `INSERT INTO blob_deletions (sha256, scope, not_before)
VALUES ($1::text, $2::text, now() + make_interval(secs => $3::float8))
ON CONFLICT (sha256, scope) DO UPDATE
SET queued_at = now(),
    deleted_xid = (pg_current_xact_id()::text)::bigint,
    not_before = EXCLUDED.not_before,
    claimed_until = NULL,
    attempts = 0`

// SetBlobDeletionGrace sets how long the bytes of a deleted blob (or of a tier
// copy a placement flip retired) are kept before the sweeper may remove them.
// Zero means as soon as no running backup can still need them.
func (p *PostgresDB) SetBlobDeletionGrace(d time.Duration) {
	p.blobDeletionGrace.Store(int64(max(d, 0)))
}

func (p *PostgresDB) deletionGraceSeconds() float64 {
	return time.Duration(p.blobDeletionGrace.Load()).Seconds()
}

// EnqueueBlobDeletion queues the copies of sha256 named by scope for deletion
// (see BlobDeletionScope*). The caller has already removed what made the copies
// live (the blobs row, or the placement on that tier) and normally holds the
// per-blob lock; a caller that cannot (a write that raced a placement flip and
// queues its own stray copy) may enqueue without it, because the sweeper
// re-checks the placement under the lock before it deletes anything. The bytes
// stay until the grace period has passed and no live backup can still need them.
func (p *PostgresDB) EnqueueBlobDeletion(ctx context.Context, sha256, scope string) error {
	err := p.q.EnqueueBlobDeletion(ctx, dbsqlc.EnqueueBlobDeletionParams{
		Sha256: sha256, Scope: scope, GraceSeconds: p.deletionGraceSeconds(),
	})
	if err != nil {
		return fmt.Errorf("enqueue blob deletion: %w", err)
	}

	return nil
}

// ClaimBlobDeletions hands out up to limit deletions that are due. A claimed
// row is invisible to other callers for claimFor (so two sweepers never take
// the same one, and a crashed sweeper's rows come back).
func (p *PostgresDB) ClaimBlobDeletions(ctx context.Context, limit int, claimFor time.Duration) ([]BlobDeletion, error) {
	rows, err := p.q.ClaimDueBlobDeletions(ctx, dbsqlc.ClaimDueBlobDeletionsParams{
		ClaimSeconds: claimFor.Seconds(), Batch: toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim blob deletions: %w", err)
	}

	out := make([]BlobDeletion, 0, len(rows))
	for _, r := range rows {
		out = append(out, BlobDeletion{
			SHA256: r.Sha256, Scope: r.Scope, QueuedAt: r.QueuedAt, NotBefore: r.NotBefore, Attempts: int(r.Attempts),
		})
	}

	return out, nil
}

// BlobDeletionDue reports whether the queued deletion may run right now: it
// exists, its grace period has passed and no live backup lease still needs the
// bytes. The sweeper asks again after taking the per-blob lock, because the
// blob may have been deleted once more (a newer deletion a backup still sees)
// since it was claimed.
func (p *PostgresDB) BlobDeletionDue(ctx context.Context, sha256, scope string) (bool, error) {
	due, err := p.q.BlobDeletionDue(ctx, dbsqlc.BlobDeletionDueParams{Sha256: sha256, Scope: scope})
	if err != nil {
		return false, fmt.Errorf("check blob deletion: %w", err)
	}

	return due, nil
}

// DeleteBlobDeletion removes a queued deletion (its bytes are gone, or it no
// longer applies).
func (p *PostgresDB) DeleteBlobDeletion(ctx context.Context, sha256, scope string) error {
	if err := p.q.DeleteBlobDeletion(ctx, dbsqlc.DeleteBlobDeletionParams{Sha256: sha256, Scope: scope}); err != nil {
		return fmt.Errorf("delete queued blob deletion: %w", err)
	}

	return nil
}

// CountBlobDeletions returns how many deletions are queued.
func (p *PostgresDB) CountBlobDeletions(ctx context.Context) (int64, error) {
	n, err := p.q.CountBlobDeletions(ctx)
	if err != nil {
		return 0, fmt.Errorf("count blob deletions: %w", err)
	}

	return n, nil
}

// PurgeExpiredBackupLeases removes the leases of backups that stopped renewing
// them (crashed). An expired lease already blocks nothing; this only tidies up.
func (p *PostgresDB) PurgeExpiredBackupLeases(ctx context.Context) (int64, error) {
	n, err := p.q.DeleteExpiredBackupLeases(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge expired backup leases: %w", err)
	}

	return n, nil
}
