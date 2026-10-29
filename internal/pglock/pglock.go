// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pglock takes the session-level Postgres advisory locks that keep a
// background job to one replica at a time.
package pglock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"
)

// unlockTimeout bounds the unlock statement, which runs on a context detached
// from the caller's so an expired job context cannot leak the lock.
const unlockTimeout = 5 * time.Second

// Try takes the session advisory lock for key (hashed by Postgres) on a
// dedicated, pinned connection, so the unlock is guaranteed to run on the same
// session. It returns acquired=false (and a nil error) when another session
// holds the lock. The release func is always non-nil and safe to call once; it
// unlocks and returns the connection to the pool, dropping the physical
// connection instead when the unlock fails, so the lock dies with the session.
func Try(ctx context.Context, db *sql.DB, key string) (func(), bool, error) {
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
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
		defer cancel()

		if _, err := conn.ExecContext(uctx, "SELECT pg_advisory_unlock(hashtext($1))", key); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}

		_ = conn.Close()
	}, true, nil
}
