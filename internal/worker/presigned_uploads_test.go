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

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type expiredUploadsDB struct {
	database.BaseMockDB

	rows    []string
	deleted []string
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
}

func (s *stagingRecorder) PresignStagingPut(context.Context, string, int64, time.Duration) (storage.PresignedPut, error) {
	return storage.PresignedPut{}, storage.ErrPresignNotSupported
}
func (s *stagingRecorder) StatStaging(context.Context, string) (int64, error) { return 0, nil }
func (s *stagingRecorder) OpenStaging(context.Context, string) (io.ReadCloser, error) {
	return nil, storage.ErrStagingNotFound
}
func (s *stagingRecorder) PromoteStaging(context.Context, string, string, map[string]string) error {
	return nil
}

func (s *stagingRecorder) DeleteStaging(_ context.Context, id string) error {
	if id == s.failFor {
		return errors.New("storage unavailable")
	}

	s.deleted = append(s.deleted, id)

	return nil
}

func TestPresignedUploadWorker_SweepRemovesStagingThenRow(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	db := &expiredUploadsDB{rows: []string{"a", "b", "c"}}
	store := &stagingRecorder{failFor: "b"}

	w := NewPresignedUploadWorker(db, store, log)

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

func TestPresignedUploadWorker_ClearsRowsWithoutStagingSupport(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	db := &expiredUploadsDB{rows: []string{"x"}}

	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if got := NewPresignedUploadWorker(db, local, log).Sweep(context.Background()); got != 1 || len(db.rows) != 0 {
		t.Fatalf("removed %d, rows left %v", got, db.rows)
	}
}

func TestPresignedUploadWorker_StartStop(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	w := NewPresignedUploadWorker(&expiredUploadsDB{}, nil, log)
	w.Start()
	w.Stop()
	w.Stop() // idempotent
}
