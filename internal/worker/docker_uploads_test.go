// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeUploadStore struct {
	stale []string
	got   time.Time
}

func (f *fakeUploadStore) DeleteStaleDockerUploads(_ context.Context, cutoff time.Time) ([]string, error) {
	f.got = cutoff

	return f.stale, nil
}

// FMTA-12: abandoned upload sessions lose their row and staged file; a fresh
// upload's file is kept; a stale file without a row is removed too.
func TestDockerUploadSweeper_RemovesAbandonedUploads(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-48 * time.Hour)

	staleRow, orphan, fresh := uuid.NewString(), uuid.NewString(), uuid.NewString()

	for _, id := range []string{staleRow, orphan, fresh} {
		if err := os.WriteFile(filepath.Join(dir, id), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, id := range []string{staleRow, orphan} {
		if err := os.Chtimes(filepath.Join(dir, id), old, old); err != nil {
			t.Fatal(err)
		}
	}

	store := &fakeUploadStore{stale: []string{staleRow}}
	s := &DockerUploadSweeper{store: store, dir: dir, ttl: 24 * time.Hour, log: testLogger(t)}
	s.Sweep(context.Background(), now)

	if want := now.Add(-24 * time.Hour); !store.got.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", store.got, want)
	}

	for id, wantExists := range map[string]bool{staleRow: false, orphan: false, fresh: true} {
		_, err := os.Stat(filepath.Join(dir, id))
		if (err == nil) != wantExists {
			t.Errorf("file %s exists=%v, want %v", id, err == nil, wantExists)
		}
	}
}
