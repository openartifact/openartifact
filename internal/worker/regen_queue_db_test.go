// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"os"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

func openReplicaPool(t *testing.T, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	return db
}

// TestRegenQueueTwoReplicasNeverOverlap simulates two replicas (two pools, two
// queues) sharing one Postgres: the advisory lock keeps the same key from
// running on both at once, and the replica that finds the lock busy retries
// instead of dropping its notification.
func TestRegenQueueTwoReplicasNeverOverlap(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	const lockKey = "regen-queue-test:shared-key"

	var (
		fl         inflight
		busy       atomic.Int64
		completedB atomic.Int64
	)

	aHolding := make(chan struct{})
	releaseA := make(chan struct{})

	mkRun := func(db *sql.DB, hold bool, completed *atomic.Int64) func(context.Context, string) error {
		return func(ctx context.Context, key string) error {
			release, err := lockForRegeneration(ctx, db, lockKey)
			if err != nil {
				busy.Add(1)
				return err
			}
			defer release()

			fl.enter()
			defer fl.leave()

			if hold {
				close(aHolding)
				<-releaseA
			}

			completed.Add(1)

			return nil
		}
	}

	var completedA atomic.Int64

	qa := newTestQueue(t, mkRun(openReplicaPool(t, dsn), true, &completedA))
	qb := newTestQueue(t, mkRun(openReplicaPool(t, dsn), false, &completedB))

	qa.Notify("k")
	<-aHolding

	// Replica B is notified while A holds the lock: it must back off and retry.
	qb.Notify("k")
	pollUntil(t, "replica B to observe the busy lock", func() bool { return busy.Load() > 0 })

	if completedB.Load() != 0 {
		t.Fatal("replica B ran while replica A held the advisory lock")
	}

	close(releaseA)
	pollUntil(t, "replica B to run after the lock was released", func() bool { return completedB.Load() == 1 })
	pollUntil(t, "both queues idle", func() bool { return qa.idle() && qb.idle() })

	if got := fl.peak.Load(); got != 1 {
		t.Errorf("same key ran concurrently on both replicas: peak=%d", got)
	}

	if got := completedA.Load(); got != 1 {
		t.Errorf("replica A completed %d runs, want 1", got)
	}
}
