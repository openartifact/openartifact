// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// verifyLockKey names the cluster-wide advisory lock (hashed by Postgres) the
// replica working on a verify pass holds, so passes of different replicas
// never overlap.
const verifyLockKey = "openartifact:replication-verify"

// tryAdvisoryLock takes a session-level Postgres advisory lock on a dedicated,
// pinned connection so the unlock runs on the same session. It returns
// acquired=false (and a nil error) when another session holds the lock. The
// returned release func is always non-nil; it unlocks with a fresh context so
// an expired caller context cannot leak the lock.
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

		// A failed unlock closes the connection below, which ends the session
		// and with it the lock.
		_, _ = conn.ExecContext(uctx, "SELECT pg_advisory_unlock(hashtext($1))", key)
		_ = conn.Close()
	}, true, nil
}
