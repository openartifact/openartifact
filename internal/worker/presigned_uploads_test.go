// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type expiredUploadsDB struct {
	databasetest.BaseMockDB

	rows    []string
	deleted []string
	known   map[string]bool // ids that have a row, for the orphan sweep
}

func (d *expiredUploadsDB) ExistingPresignedUploadIDs(_ context.Context, ids []string) ([]string, error) {
	var found []string

	for _, id := range ids {
		if d.known[id] {
			found = append(found, id)
		}
	}

	return found, nil
}

func (d *expiredUploadsDB) ListExpiredPresignedUploads(_ context.Context, limit int) ([]string, error) {
	return append([]string(nil), d.rows[:min(limit, len(d.rows))]...), nil
}

func (d *expiredUploadsDB) DeletePresignedUpload(_ context.Context, id string) error {
	d.deleted = append(d.deleted, id)

	var kept []string

	for _, r := range d.rows {
		if r != id {
			kept = append(kept, r)
		}
	}

	d.rows = kept

	return nil
}

type stagingRecorder struct {
	storage.BlobStore

	deleted []string
	failFor string
	// objects are the staging objects the walk reports: id -> last modified.
	objects map[string]time.Time
}

func (s *stagingRecorder) StagingSupported() bool { return true }

func (s *stagingRecorder) PresignStagingPut(context.Context, string, int64, time.Duration) (storage.PresignedPut, error) {
	return storage.PresignedPut{}, storage.ErrPresignNotSupported
}

func (s *stagingRecorder) StatStaging(context.Context, string) (storage.StagedObject, error) {
	return storage.StagedObject{}, storage.ErrStagingNotFound
}

func (s *stagingRecorder) OpenStaging(context.Context, string, string) (io.ReadCloser, error) {
	return nil, storage.ErrStagingNotFound
}

func (s *stagingRecorder) PromoteStaging(context.Context, string, string, string, map[string]string) error {
	return nil
}

func (s *stagingRecorder) DeleteStaging(_ context.Context, id string) error {
	if id == s.failFor {
		return errors.New("storage unavailable")
	}

	s.deleted = append(s.deleted, id)
	delete(s.objects, id)

	return nil
}

func (s *stagingRecorder) WalkStaging(_ context.Context, olderThan time.Time, fn func([]string) error) error {
	var ids []string

	for id, modified := range s.objects {
		if modified.Before(olderThan) {
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		return nil
	}

	return fn(ids)
}

const testURLTTL = 30 * time.Minute

func TestPresignedUploadWorker_SweepRemovesStagingThenRow(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	db := &expiredUploadsDB{rows: []string{"a", "b", "c"}}
	store := &stagingRecorder{failFor: "b"}

	w := NewPresignedUploadWorker(db, store, testURLTTL, log)

	if got := w.Sweep(context.Background()); got != 2 {
		t.Fatalf("Sweep removed %d uploads, want 2", got)
	}

	if len(store.deleted) != 2 || len(db.deleted) != 2 {
		t.Errorf("staging deletes %v, row deletes %v", store.deleted, db.deleted)
	}

	// The upload whose staging object could not be removed keeps its row so the
	// next sweep retries; deleting the row first would leak the object forever.
	if len(db.rows) != 1 || db.rows[0] != "b" {
		t.Errorf("remaining rows = %v, want [b]", db.rows)
	}

	store.failFor = ""

	if got := w.Sweep(context.Background()); got != 1 || len(db.rows) != 0 {
		t.Errorf("retry removed %d, rows left %v", got, db.rows)
	}
}

func TestPresignedUploadWorker_SweepsOrphanedStagingObjects(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	old := time.Now().Add(-48 * time.Hour)
	db := &expiredUploadsDB{known: map[string]bool{"with-row": true}}
	store := &stagingRecorder{objects: map[string]time.Time{
		"orphan":     old,                          // re-PUT after its row was removed
		"with-row":   old,                          // still referenced: kept
		"fresh":      time.Now().Add(-time.Minute), // could still belong to an upload being created
		"orphan-too": old,
	}}

	if got := NewPresignedUploadWorker(db, store, testURLTTL, log).Sweep(context.Background()); got != 2 {
		t.Fatalf("Sweep removed %d, want the 2 orphans", got)
	}

	if _, ok := store.objects["with-row"]; !ok {
		t.Error("a staging object with a row was deleted")
	}

	if _, ok := store.objects["fresh"]; !ok {
		t.Error("a staging object younger than the URL TTL plus grace was deleted")
	}

	if len(store.objects) != 2 {
		t.Errorf("objects left = %v", store.objects)
	}
}

func TestPresignedUploadWorker_ClearsRowsWithoutStagingSupport(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	db := &expiredUploadsDB{rows: []string{"x"}}

	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if got := NewPresignedUploadWorker(db, local, testURLTTL, log).Sweep(context.Background()); got != 1 || len(db.rows) != 0 {
		t.Fatalf("removed %d, rows left %v", got, db.rows)
	}
}

func TestPresignedUploadWorker_StartStop(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	w := NewPresignedUploadWorker(&expiredUploadsDB{}, nil, testURLTTL, log)
	w.Start()
	w.Stop()
	w.Stop() // idempotent
}
