// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
	"testing/fstest"
	"time"
)

// A replica waiting for the migration lock must not deadlock with the replica
// holding it while that one runs CREATE INDEX CONCURRENTLY (a .no_tx
// migration): CIC waits for every open snapshot, including a statement
// blocked in pg_advisory_lock.
func TestMigrateWaiterDoesNotDeadlockConcurrentIndex(t *testing.T) {
	db := scratchDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	mustExec(t, db, `CREATE TABLE cic_target (id int)`)

	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()

	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_lock(`+advisoryLockKey+`)`); err != nil {
		t.Fatal(err)
	}

	waiter := newMigratorWithFS(db, fstest.MapFS{
		"001_noop.sql": {Data: []byte(`SELECT 1;`)},
	})

	done := make(chan error, 1)
	go func() { done <- waiter.Apply(ctx) }()

	// Wait until the waiter has a session on the server, then build an index
	// concurrently while still holding the lock, as a migrating replica does.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := holder.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&n); err != nil {
			t.Fatal(err)
		}

		if n > 0 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("waiter never connected")
		}

		time.Sleep(20 * time.Millisecond)
	}

	if _, err := holder.ExecContext(ctx, `CREATE INDEX CONCURRENTLY cic_target_id ON cic_target (id)`); err != nil {
		t.Fatalf("CREATE INDEX CONCURRENTLY with a waiting migrator: %v", err)
	}

	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_unlock(`+advisoryLockKey+`)`); err != nil {
		t.Fatal(err)
	}

	if err := <-done; err != nil {
		t.Fatalf("waiting migrator: %v", err)
	}
}
