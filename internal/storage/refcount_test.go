// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
)

// orphanDB lists a fixed set of unreferenced blobs; BlobReferenceCount reports
// zero, so SafeDeleteBlob takes its blobStore.Delete path.
type orphanDB struct {
	database.BaseMockDB

	shas       []string
	gotMinAge  time.Duration
	gotLimit   int
	refCountOf map[string]int
}

func (d *orphanDB) ListUnreferencedBlobs(_ context.Context, minAge time.Duration, _ string, _, limit int) ([]string, string, error) {
	d.gotMinAge, d.gotLimit = minAge, limit

	return d.shas, "", nil
}

func (d *orphanDB) BlobReferenceCount(_ context.Context, sha string) (int, error) {
	return d.refCountOf[sha], nil
}

// deleteRecorder is a BlobStore that only records (and can fail) deletes.
type deleteRecorder struct {
	BlobStore

	deleted []string
	failOn  string
}

func (s *deleteRecorder) Delete(_ context.Context, id string) error {
	if id == s.failOn {
		return errors.New("storage unavailable")
	}

	s.deleted = append(s.deleted, id)

	return nil
}

func TestCollectOrphanBlobsSurvivesPoisonedBlob(t *testing.T) {
	good1 := strings.Repeat("a", 64)
	bad := strings.Repeat("b", 64)
	good2 := strings.Repeat("c", 64)
	live := strings.Repeat("d", 64)

	db := &orphanDB{shas: []string{good1, bad, live, good2}, refCountOf: map[string]int{live: 1}}
	store := &deleteRecorder{failOn: bad}

	removed, err := CollectOrphanBlobs(context.Background(), db, store, OrphanMinAge, OrphanBatch)
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("err = %v, want the failure for %s", err, bad)
	}

	if removed != 3 { // good1, live (left alone but not an error), good2
		t.Errorf("removed = %d, want 3", removed)
	}

	if got := strings.Join(store.deleted, ","); got != good1+","+good2 {
		t.Errorf("deleted = %s; want only the unreferenced, healthy blobs (%s,%s)", got, good1, good2)
	}

	if db.gotMinAge != time.Hour || db.gotLimit != OrphanBatch {
		t.Errorf("listed with minAge=%v limit=%d; want 1h/%d", db.gotMinAge, db.gotLimit, OrphanBatch)
	}
}

func TestCollectOrphanBlobsNoopWithoutLister(t *testing.T) {
	removed, err := CollectOrphanBlobs(context.Background(), &database.BaseMockDB{}, &deleteRecorder{}, time.Hour, 10)
	if removed != 0 || err != nil {
		t.Errorf("removed=%d err=%v; want a no-op", removed, err)
	}
}

// queueingDB has the deferred-deletion path: SafeDeleteBlob must hand the blob
// to it and leave the bytes alone.
type queueingDB struct {
	database.BaseMockDB

	queued []string
}

func (d *queueingDB) DeleteBlobIfUnreferenced(_ context.Context, sha string) (bool, error) {
	d.queued = append(d.queued, sha)

	return true, nil
}

func TestSafeDeleteBlobDefersByteRemovalToTheQueue(t *testing.T) {
	sha := strings.Repeat("a", 64)
	db := &queueingDB{}
	store := &deleteRecorder{}

	if err := SafeDeleteBlob(context.Background(), db, store, sha); err != nil {
		t.Fatalf("SafeDeleteBlob: %v", err)
	}

	if len(db.queued) != 1 || db.queued[0] != sha {
		t.Errorf("queued = %v, want [%s]", db.queued, sha)
	}

	if len(store.deleted) != 0 {
		t.Errorf("SafeDeleteBlob deleted bytes immediately: %v", store.deleted)
	}
}
