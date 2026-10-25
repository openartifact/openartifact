// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type orphanListerDB struct {
	databasetest.BaseMockDB

	calls  int
	minAge time.Duration
}

func (d *orphanListerDB) ListUnreferencedBlobs(_ context.Context, minAge time.Duration, _ string, _, _ int) ([]string, string, error) {
	d.calls++
	d.minAge = minAge

	return []string{strings.Repeat("e", 64)}, "", nil
}

func (d *orphanListerDB) BlobReferenceCount(context.Context, string) (int, error) { return 0, nil }

type countingStore struct {
	storage.BlobStore

	deletes int
}

func (s *countingStore) Delete(context.Context, string) error {
	s.deletes++

	return nil
}

func TestCollectOrphansIfDueIsThrottled(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &orphanListerDB{}
	store := &countingStore{}
	w := NewCleanupWorker(db, store, log)

	now := time.Now()

	w.collectOrphansIfDue(context.Background(), now)

	if db.calls != 1 || store.deletes != 1 || db.minAge != storage.OrphanMinAge {
		t.Fatalf("first tick: list calls=%d deletes=%d minAge=%v; want 1/1/%v", db.calls, store.deletes, db.minAge, storage.OrphanMinAge)
	}

	w.collectOrphansIfDue(context.Background(), now.Add(orphanInterval-time.Second))

	if db.calls != 1 {
		t.Errorf("collection ran again within %v (calls=%d)", orphanInterval, db.calls)
	}

	w.collectOrphansIfDue(context.Background(), now.Add(orphanInterval))

	if db.calls != 2 {
		t.Errorf("collection did not run after %v (calls=%d)", orphanInterval, db.calls)
	}
}

type reconcileCountingDB struct {
	databasetest.BaseMockDB

	calls     int
	corrected int64
	err       error
}

func (d *reconcileCountingDB) ReconcileAllStorageUsage(context.Context) (int64, error) {
	d.calls++

	return d.corrected, d.err
}

func TestReconcileQuotasIfDueIsThrottled(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &reconcileCountingDB{corrected: 2}
	w := NewCleanupWorker(db, nil, log)
	now := time.Now()

	w.reconcileQuotasIfDue(context.Background(), now)

	if db.calls != 1 {
		t.Fatalf("first tick: reconcile calls = %d, want 1 (the first tick after startup repairs crash drift)", db.calls)
	}

	w.reconcileQuotasIfDue(context.Background(), now.Add(quotaReconcileInterval-time.Second))

	if db.calls != 1 {
		t.Errorf("reconcile ran again within %v (calls=%d)", quotaReconcileInterval, db.calls)
	}

	w.reconcileQuotasIfDue(context.Background(), now.Add(quotaReconcileInterval))

	if db.calls != 2 {
		t.Errorf("reconcile did not run after %v (calls=%d)", quotaReconcileInterval, db.calls)
	}
}

func TestReconcileQuotasIfDueWaitsAfterFailure(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &reconcileCountingDB{err: context.DeadlineExceeded}
	w := NewCleanupWorker(db, nil, log)
	now := time.Now()

	w.reconcileQuotasIfDue(context.Background(), now)
	w.reconcileQuotasIfDue(context.Background(), now.Add(time.Minute))

	if db.calls != 1 {
		t.Errorf("a failed reconcile retried after one minute (calls=%d); it must wait for the next interval", db.calls)
	}
}

func TestReconcileQuotasIfDueSkipsCancelledContext(t *testing.T) {
	db := &reconcileCountingDB{}
	w := NewCleanupWorker(db, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w.reconcileQuotasIfDue(ctx, time.Now())

	if db.calls != 0 {
		t.Errorf("reconciled with a cancelled context (calls=%d)", db.calls)
	}
}

func TestCollectOrphansIfDueSkipsWithoutBlobStore(t *testing.T) {
	db := &orphanListerDB{}
	w := NewCleanupWorker(db, nil, nil)

	w.collectOrphansIfDue(context.Background(), time.Now())

	if db.calls != 0 {
		t.Errorf("listed orphans without a blob store")
	}
}
