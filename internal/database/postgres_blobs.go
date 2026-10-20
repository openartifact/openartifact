// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
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

	return tx.Commit()
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

// ListUnreferencedBlobs returns up to limit sha256 values of blobs rows that no
// package row references and that are older than minAge (so in-flight uploads,
// which create the blobs row before their referencing row, are never picked).
// Used to reclaim storage after repository deletion cascades its package rows.
func (p *PostgresDB) ListUnreferencedBlobs(ctx context.Context, minAge time.Duration, limit int) ([]string, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT b.sha256 FROM blobs b
		 WHERE b.created_at < now() - make_interval(secs => $1)
		   AND (`+blobRefSumExpr+`) = 0
		 ORDER BY b.created_at
		 LIMIT $2`, minAge.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("list unreferenced blobs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("scan unreferenced blob: %w", err)
		}

		out = append(out, sha)
	}

	return out, rows.Err()
}
