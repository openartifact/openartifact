//go:build integration

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"context"
	"testing"
	"time"
)

func workerBacklog(t *testing.T, j *Journal) int64 {
	t.Helper()

	st, err := j.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	return st.Backlog
}

func TestWorkerRunPass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		lockHeld bool
		wantRan  bool
		wantLeft int64
	}{
		{"holds the lock and drains", false, true, 0},
		{"another replica holds the lock", true, false, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := newTestDB(t)
			store := newFSStore(t)
			seedRaw(t, db, store, "wk", "a.bin", []byte("worker"))

			j := newTestJournal(t, db, store)
			w := NewWorker(j, testLog(t), time.Hour)
			w.lock = func(context.Context) (func(), bool, error) { return func() {}, !tc.lockHeld, nil }

			if got := w.RunPass(context.Background()); got != tc.wantRan {
				t.Fatalf("RunPass ran=%t want %t", got, tc.wantRan)
			}

			if left := workerBacklog(t, j); left < tc.wantLeft || (tc.wantLeft == 0 && left != 0) {
				t.Fatalf("backlog=%d want %d", left, tc.wantLeft)
			}
		})
	}
}

func TestWorkerLockIsExclusive(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	j := newTestJournal(t, db, newFSStore(t))

	first := NewWorker(j, testLog(t), time.Hour)
	second := NewWorker(j, testLog(t), time.Hour)

	release, ok, err := first.lock(context.Background())
	if err != nil || !ok {
		t.Fatalf("first lock: ok=%t err=%v", ok, err)
	}

	if _, ok, err := second.lock(context.Background()); err != nil || ok {
		t.Fatalf("second replica acquired the lock too: ok=%t err=%v", ok, err)
	}

	release()

	rel2, ok, err := second.lock(context.Background())
	if err != nil || !ok {
		t.Fatalf("lock not released: ok=%t err=%v", ok, err)
	}

	rel2()
}

func TestWorkerStartDrainsAndStops(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	store := newFSStore(t)
	seedRaw(t, db, store, "wk2", "b.bin", []byte("loop"))

	j := newTestJournal(t, db, store)
	w := NewWorker(j, testLog(t), 20*time.Millisecond)
	w.Start()

	deadline := time.Now().Add(30 * time.Second)
	for workerBacklog(t, j) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the worker never drained the queue")
		}

		time.Sleep(25 * time.Millisecond)
	}

	w.Stop()
	w.Stop() // idempotent

	if w.Running() {
		t.Fatal("worker still running after Stop")
	}

	var nilWorker *Worker

	nilWorker.Stop() // a disabled journal leaves a nil worker in the stop list
}
