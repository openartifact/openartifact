// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type orphanListerDB struct {
	database.BaseMockDB

	calls  int
	minAge time.Duration
}

func (d *orphanListerDB) ListUnreferencedBlobs(_ context.Context, minAge time.Duration, _ int) ([]string, error) {
	d.calls++
	d.minAge = minAge

	return []string{strings.Repeat("e", 64)}, nil
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

func TestCollectOrphansIfDueSkipsWithoutBlobStore(t *testing.T) {
	db := &orphanListerDB{}
	w := NewCleanupWorker(db, nil, nil)

	w.collectOrphansIfDue(context.Background(), time.Now())

	if db.calls != 0 {
		t.Errorf("listed orphans without a blob store")
	}
}
