// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

// repoStatsFake is a RepoStatsStore that is also a database.RepoStatsReconciler:
// it has a backlog of repositories and recomputes up to batch of them per call.
type repoStatsFake struct {
	mu      sync.Mutex
	backlog int
	calls   []int
	maxAges []time.Duration
	err     error
}

func (*repoStatsFake) GetDB() *sql.DB { return nil }

func (f *repoStatsFake) ReconcileRepoStats(_ context.Context, batch int, maxAge time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return 0, f.err
	}

	n := min(f.backlog, batch)
	f.backlog -= n
	f.calls = append(f.calls, n)
	f.maxAges = append(f.maxAges, maxAge)

	return n, nil
}

func (f *repoStatsFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.calls)
}

func TestRepoStatsPassDrainsTheBacklogInBatches(t *testing.T) {
	db := &repoStatsFake{backlog: 2*repoStatsBatch + 3}
	w := NewRepoStatsWorker(db, testLog(t), time.Minute)

	pass, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if pass.Repositories != 2*repoStatsBatch+3 || pass.Busy {
		t.Fatalf("pass = %+v, want every queued repository recomputed", pass)
	}

	if len(db.calls) != 3 {
		t.Fatalf("%d reconcile calls %v, want 3 (two full batches and a short one)", len(db.calls), db.calls)
	}

	if db.maxAges[0] != repoStatsMaxAge {
		t.Fatalf("drift age %v, want %v", db.maxAges[0], repoStatsMaxAge)
	}

	// A full last batch needs one more call to learn that nothing is left.
	db.backlog = repoStatsBatch
	if _, err := w.RunOnce(context.Background()); err != nil || len(db.calls) != 5 {
		t.Fatalf("calls %v err %v, want a full batch followed by an empty one", db.calls, err)
	}
}

func TestRepoStatsPassSkippedWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	db := &repoStatsFake{backlog: 5}
	w := NewRepoStatsWorker(db, testLog(t), time.Minute)
	w.lock = func(context.Context) (func(), bool, error) { return func() {}, false, nil }

	pass, err := w.RunOnce(context.Background())
	if err != nil || !pass.Busy || db.callCount() != 0 {
		t.Fatalf("pass %+v err %v calls %d, want Busy and no work", pass, err, db.callCount())
	}
}

func TestRepoStatsPassReleasesTheLockAndReportsFailures(t *testing.T) {
	boom := errors.New("boom")
	db := &repoStatsFake{err: boom}
	w := NewRepoStatsWorker(db, testLog(t), time.Minute)

	released := false
	w.lock = func(context.Context) (func(), bool, error) { return func() { released = true }, true, nil }

	if _, err := w.RunOnce(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's error", err)
	}

	if !released {
		t.Fatal("the lock was not released after a failed pass")
	}
}

func TestRepoStatsPassStopsWhenTheContextEnds(t *testing.T) {
	db := &repoStatsFake{backlog: 10 * repoStatsBatch}
	w := NewRepoStatsWorker(db, testLog(t), time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := w.RunOnce(ctx); err == nil || db.callCount() != 0 {
		t.Fatalf("err %v calls %d, want a cancelled pass that did nothing", err, db.callCount())
	}
}

func TestRepoStatsWorkerRunsAtStartAndStops(t *testing.T) {
	db := &repoStatsFake{backlog: 7}
	w := NewRepoStatsWorker(db, testLog(t), 10*time.Millisecond)

	w.Start()

	deadline := time.After(5 * time.Second)

	for db.callCount() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d passes ran", db.callCount())
		case <-time.After(5 * time.Millisecond):
		}
	}

	w.Stop()

	n := db.callCount()

	time.Sleep(50 * time.Millisecond)

	if db.callCount() != n {
		t.Fatal("the worker kept running after Stop")
	}

	if db.calls[0] != 7 {
		t.Fatalf("first pass recomputed %d, want the whole backlog of 7 at start", db.calls[0])
	}
}

func TestRepoStatsWorkerDisabledWithoutInterval(t *testing.T) {
	db := &repoStatsFake{backlog: 7}
	w := NewRepoStatsWorker(db, testLog(t), 0)

	w.Start()
	w.Stop()

	if db.callCount() != 0 {
		t.Fatal("a worker without an interval must not run")
	}
}
