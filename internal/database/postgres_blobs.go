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
)

// acquireLockSlot reserves one of the blob-lock slots (see lockSlots) and
// returns the function that gives it back.
func (p *PostgresDB) acquireLockSlot(ctx context.Context) (func(), error) {
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
func (p *PostgresDB) WithBlobLock(ctx context.Context, sha256 string, fn func(ctx context.Context) error) error {
	return p.WithBlobLocks(ctx, []string{sha256}, fn)
}

// WithBlobLocks is WithBlobLock for several blobs at once: all locks are taken
// on ONE transaction connection, in sorted order (so two callers locking
// overlapping sets cannot deadlock each other), and held until fn returns.
// Taking them through nested WithBlobLock calls instead would pin one pooled
// connection per lock and let concurrent callers exhaust the pool.
func (p *PostgresDB) WithBlobLocks(ctx context.Context, shas []string, fn func(ctx context.Context) error) error {
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
// calls deleteStorage, but only when no package row still references it. The
// check, row delete and storage delete run under the blob advisory lock; if
// storage deletion fails the row delete is rolled back. Returns true when the
// blob was removed.
func (p *PostgresDB) DeleteBlobIfUnreferenced(ctx context.Context, sha256 string, deleteStorage func(ctx context.Context) error) (bool, error) {
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

	if err := deleteStorage(ctx); err != nil {
		return false, err
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
func (p *PostgresDB) ListUnreferencedBlobs(ctx context.Context, minAge time.Duration, after string, scan, limit int) ([]string, string, error) {
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
