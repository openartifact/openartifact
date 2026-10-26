// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestAdvisoryLockExcludesAcrossSessions: a held key is busy for every other
// session (errLockBusy, which the regen queue retries), other keys are free,
// and the lock is gone as soon as it is released, even when the context that
// took it has been cancelled in the meantime.
func TestAdvisoryLockExcludesAcrossSessions(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	poolA, poolB := openReplicaPool(t, dsn), openReplicaPool(t, dsn)
	key := "lock-test:" + time.Now().Format(time.RFC3339Nano)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release, err := lockForRegeneration(ctx, poolA, key)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	if _, err := lockForRegeneration(context.Background(), poolB, key); !errors.Is(err, errLockBusy) {
		t.Fatalf("lock held by another session = %v, want errLockBusy", err)
	}

	// The same pool is another session too: locks are not reentrant across pinned connections.
	if _, err := lockForRegeneration(context.Background(), poolA, key); !errors.Is(err, errLockBusy) {
		t.Fatalf("second lock from the holder's pool = %v, want errLockBusy", err)
	}

	otherRelease, err := lockForRegeneration(context.Background(), poolB, key+":other")
	if err != nil {
		t.Fatalf("an unrelated key is locked: %v", err)
	}

	otherRelease()

	// Releasing with an already cancelled context must still free the lock.
	cancel()
	release()

	again, err := lockForRegeneration(context.Background(), poolB, key)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}

	again()

	// A dead context is an error, not a "busy" answer to retry.
	dead, stop := context.WithCancel(context.Background())
	stop()

	if _, err := lockForRegeneration(dead, poolA, key); err == nil || errors.Is(err, errLockBusy) {
		t.Fatalf("lock with a cancelled context = %v, want a plain error", err)
	}
}
