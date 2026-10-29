// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// BlobRefStore owns the blobs table: upsert, reference counting, blob locks and the deferred-deletion queue.
type BlobRefStore struct{ *core }

// acquireLockSlot reserves one of the blob-lock slots (see lockSlots) and
// returns the function that gives it back.
func (p *core) acquireLockSlot(ctx context.Context) (func(), error) {
	if p.lockSlots == nil {
		return func() {}, nil
	}

	select {
	case p.lockSlots <- struct{}{}:
		return func() { <-p.lockSlots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for blob lock slot: %w", ctx.Err())
	}
}

// WithBlobLock runs fn inside a transaction holding a transaction-scoped
// advisory lock keyed on the blob sha256. Uploaders that create a reference to
// a blob and the garbage collector (DeleteBlobIfUnreferenced) serialize on the
// same lock, so a blob cannot be deleted between "ref count == 0" and the
// uploader's insert.
func (p *BlobRefStore) WithBlobLock(ctx context.Context, sha256 string, fn func(ctx context.Context) error) error {
	return p.WithBlobLocks(ctx, []string{sha256}, fn)
}

// WithBlobLocks is WithBlobLock for several blobs at once: all locks are taken
// on ONE transaction connection, in sorted order (so two callers locking
// overlapping sets cannot deadlock each other), and held until fn returns.
// Taking them through nested WithBlobLock calls instead would pin one pooled
// connection per lock and let concurrent callers exhaust the pool.
func (p *BlobRefStore) WithBlobLocks(ctx context.Context, shas []string, fn func(ctx context.Context) error) error {
	keys := slices.Clone(shas)
	slices.Sort(keys)
	keys = slices.Compact(keys)

	release, err := p.acquireLockSlot(ctx)
	if err != nil {
		return err
	}
	defer release()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin blob lock tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	for _, sha := range keys {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('blob:' || $1, 0))`, sha); err != nil {
			return fmt.Errorf("acquire blob lock: %w", err)
		}
	}

	if err := fn(ctx); err != nil {
		return err
	}

	// The lock transaction writes nothing; fn has already committed its own
	// work. If the session was killed meanwhile (idle_in_transaction_session_timeout
	// on a long upload), failing here would make the caller delete a blob a
	// committed row now references, so report it instead of returning an error.
	if err := tx.Commit(); err != nil {
		p.log.Warnw("blob lock transaction ended before fn returned; the lock may have been lost", "error", err)
	}

	return nil
}

// DeleteBlobIfUnreferenced atomically removes the blobs row for sha256 and
// queues its bytes for deletion (BlobDeletionScopeAll), but only when no
// package row still references it. The check, row delete and enqueue run in
// one transaction under the blob advisory lock; the bytes are removed later by
// the sweeper, once the grace period has passed and no running backup can still
// need them. Returns true when the blob was removed.
func (p *BlobRefStore) DeleteBlobIfUnreferenced(ctx context.Context, sha256 string) (bool, error) {
	release, err := p.acquireLockSlot(ctx)
	if err != nil {
		return false, err
	}
	defer release()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin blob delete tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('blob:' || $1, 0))`, sha256); err != nil {
		return false, fmt.Errorf("acquire blob lock: %w", err)
	}

	count, err := blobRefCount(ctx, tx, sha256)
	if err != nil {
		return false, err
	}

	if count > 0 {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE sha256 = $1`, sha256); err != nil {
		if IsForeignKeyViolation(err) {
			return false, nil // still referenced by a table not covered by the count
		}

		return false, fmt.Errorf("delete blobs row: %w", err)
	}

	if _, err := tx.ExecContext(ctx, enqueueBlobDeletionSQL, sha256, BlobDeletionScopeAll, p.deletionGraceSeconds()); err != nil {
		return false, fmt.Errorf("queue blob bytes for deletion: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit blob delete: %w", err)
	}

	return true, nil
}

// ListUnreferencedBlobs scans one window of at most scan blobs rows whose
// sha256 sorts after the cursor and returns up to limit of them that no
// package row references and that are older than minAge (so in-flight uploads,
// which create the blobs row before their referencing row, are never picked).
// next is the cursor for the following call and is "" once the end of the
// table was reached, i.e. the scan wraps around.
//
// Bounding the window (instead of evaluating the reference check for every old
// blob on every call) keeps the cost of a pass independent of table size, and
// advancing by sha256 means orphans that cannot be removed never block the
// ones behind them. Used to reclaim storage after repository deletion cascades
// its package rows.
func (p *BlobRefStore) ListUnreferencedBlobs(
	ctx context.Context,
	minAge time.Duration,
	after string,
	scan, limit int,
) ([]string, string, error) {
	// Upper bound of the window: the scan-th sha after the cursor.
	var upper sql.NullString

	err := p.db.QueryRowContext(ctx,
		`SELECT sha256 FROM blobs WHERE sha256 > $1 ORDER BY sha256 OFFSET $2 LIMIT 1`, after, scan-1,
	).Scan(&upper)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("list unreferenced blobs: window: %w", err)
	}

	rows, err := p.db.QueryContext(ctx,
		`SELECT b.sha256 FROM blobs b
		 WHERE b.sha256 > $1 AND ($2::text IS NULL OR b.sha256 <= $2::text)
		   AND b.created_at < now() - make_interval(secs => $3)
		   AND `+blobUnreferencedExpr+`
		 ORDER BY b.sha256
		 LIMIT $4`, after, upper, minAge.Seconds(), limit)
	if err != nil {
		return nil, "", fmt.Errorf("list unreferenced blobs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, "", fmt.Errorf("scan unreferenced blob: %w", err)
		}

		out = append(out, sha)
	}

	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list unreferenced blobs: %w", err)
	}

	next := ""

	switch {
	case len(out) >= limit:
		// The window was cut short by the limit; resume right after the last hit.
		next = out[len(out)-1]
	case upper.Valid:
		next = upper.String
	}

	return out, next, nil
}

// EnsureBlob ensures the global blobs table has a row for sha256 (creating
// it if it does not exist yet) and returns its
// blobs.id. Manifests are stored directly in the blob store without a
// per-repo registry row (unlike docker_blobs), so this is the manifest
// handlers' entry point into upsertBlobWithMediaType.
func (p *BlobRefStore) EnsureBlob(ctx context.Context, sha256, mediaType string, size int64) (string, error) {
	blobID, err := p.upsertBlobWithMediaType(ctx, sha256, sha256, mediaType, size)
	if err != nil {
		return "", fmt.Errorf("failed to ensure blob: %w", err)
	}

	return blobID, nil
}

// UpsertBlob inserts a blob row keyed by sha256 and returns its id, using ON
// CONFLICT to handle concurrent uploads safely.
func (p *BlobRefStore) UpsertBlob(ctx context.Context, sha256, blobKey string, sizeBytes int64) (string, error) {
	if err := validateBlobSHA256(sha256); err != nil {
		return "", err
	}

	var blobID string
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO blobs (sha256, size_bytes, blob_key)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (sha256) DO UPDATE SET
		     size_bytes = CASE WHEN blobs.size_bytes = 0 THEN EXCLUDED.size_bytes ELSE blobs.size_bytes END
		 RETURNING id`,
		sha256, sizeBytes, blobKey,
	).Scan(&blobID)
	if err != nil {
		return "", fmt.Errorf("upsert blob: %w", err)
	}
	return blobID, nil
}

func (p *BlobRefStore) ArtifactExists(ctx context.Context, sha256 string, repositoryID string) (bool, error) {
	var exists bool
	err := p.db.QueryRowContext(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM package_files f JOIN blobs b ON b.id = f.blob_id
			WHERE b.sha256 = $1 AND f.repository_id = $2 AND f.deleted_at IS NULL)`,
		sha256,
		repositoryID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check artifact existence: %w", err)
	}

	return exists, nil
}

// BlobReferenceCount returns the number of rows, across package_files and the
// other tables that store a blob_id FK into the global blobs table, referencing the blob
// identified by sha256. A count of 0 means the blob is safe to delete from
// storage (see SafeDeleteBlob in internal/storage/refcount.go). A blob that
// does not exist in the blobs table is still checked against the tables that
// reference storage objects by sha256 alone (generated metadata, deltas,
// release bundles, proxy cache).
func (p *BlobRefStore) BlobReferenceCount(ctx context.Context, sha256 string) (int, error) {
	return blobRefCount(ctx, p.db, sha256)
}

// blobRefSumExpr is the SQL sum of every reference to blob alias b.
var blobRefSumExpr = buildBlobRefExpr(" +\n", "(SELECT COUNT(*) FROM %s WHERE %s)") //nolint:gochecknoglobals // built once, read-only

func blobRefCount(ctx context.Context, q rowQuerier, sha256 string) (int, error) {
	var count int

	err := q.QueryRowContext(ctx,
		`SELECT
			`+blobRefSumExpr+`
		 FROM (SELECT $1::text AS sha256, (SELECT id FROM blobs WHERE sha256 = $1::text) AS id) b`,
		sha256,
	).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("failed to count blob references: %w", err)
	}

	return count, nil
}

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
func (p *BlobRefStore) SetBlobDeletionGrace(d time.Duration) {
	p.blobDeletionGrace.Store(int64(max(d, 0)))
}

func (p *BlobRefStore) deletionGraceSeconds() float64 {
	return time.Duration(p.blobDeletionGrace.Load()).Seconds()
}

// EnqueueBlobDeletion queues the copies of sha256 named by scope for deletion
// (see BlobDeletionScope*). The caller has already removed what made the copies
// live (the blobs row, or the placement on that tier) and normally holds the
// per-blob lock; a caller that cannot (a write that raced a placement flip and
// queues its own stray copy) may enqueue without it, because the sweeper
// re-checks the placement under the lock before it deletes anything. The bytes
// stay until the grace period has passed and no live backup can still need them.
func (p *BlobRefStore) EnqueueBlobDeletion(ctx context.Context, sha256, scope string) error {
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
func (p *BlobRefStore) ClaimBlobDeletions(ctx context.Context, limit int, claimFor time.Duration) ([]BlobDeletion, error) {
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
func (p *BlobRefStore) BlobDeletionDue(ctx context.Context, sha256, scope string) (bool, error) {
	due, err := p.q.BlobDeletionDue(ctx, dbsqlc.BlobDeletionDueParams{Sha256: sha256, Scope: scope})
	if err != nil {
		return false, fmt.Errorf("check blob deletion: %w", err)
	}

	return due, nil
}

// DeleteBlobDeletion removes a queued deletion (its bytes are gone, or it no
// longer applies).
func (p *BlobRefStore) DeleteBlobDeletion(ctx context.Context, sha256, scope string) error {
	if err := p.q.DeleteBlobDeletion(ctx, dbsqlc.DeleteBlobDeletionParams{Sha256: sha256, Scope: scope}); err != nil {
		return fmt.Errorf("delete queued blob deletion: %w", err)
	}

	return nil
}

// CountBlobDeletions returns how many deletions are queued.
func (p *BlobRefStore) CountBlobDeletions(ctx context.Context) (int64, error) {
	n, err := p.q.CountBlobDeletions(ctx)
	if err != nil {
		return 0, fmt.Errorf("count blob deletions: %w", err)
	}

	return n, nil
}
