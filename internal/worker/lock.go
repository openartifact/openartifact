// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"
)

// lockForRegeneration takes the advisory lock for key on a pinned connection
// held until the returned release func runs. A lock held by another session
// yields errLockBusy so the regeneration queue retries the key later instead of
// dropping it. The connection is only held while the lock is.
func lockForRegeneration(ctx context.Context, db *sql.DB, key string) (func(), error) {
	release, locked, err := tryAdvisoryLock(ctx, db, key)
	if err != nil {
		return nil, fmt.Errorf("advisory lock %s: %w", key, err)
	}

	if !locked {
		return nil, fmt.Errorf("advisory lock %s: %w", key, errLockBusy)
	}

	return release, nil
}

// tryAdvisoryLock takes a session-level Postgres advisory lock on a dedicated,
// pinned connection so the unlock is guaranteed to run on the same session.
// It returns acquired=false (and a nil error) if another session holds the lock.
// The returned release func is always non-nil and safe to call once; it unlocks
// with a fresh context so an expired job context cannot leak the lock.
func tryAdvisoryLock(ctx context.Context, db *sql.DB, key string) (func(), bool, error) {
	noop := func() {}

	conn, err := db.Conn(ctx)
	if err != nil {
		return noop, false, fmt.Errorf("pin connection for advisory lock: %w", err)
	}

	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&locked); err != nil {
		_ = conn.Close()
		return noop, false, fmt.Errorf("advisory lock query: %w", err)
	}

	if !locked {
		_ = conn.Close()
		return noop, false, nil
	}

	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if _, err := conn.ExecContext(uctx, "SELECT pg_advisory_unlock(hashtext($1))", key); err != nil {
			// Unlock failed: drop the physical connection so the session lock dies with it.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}

		_ = conn.Close()
	}, true, nil
}
