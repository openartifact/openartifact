// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

type errUploadStore struct{ fakeUploadStore }

func (*errUploadStore) DeleteStaleDockerUploads(context.Context, time.Time) ([]string, error) {
	return nil, errors.New("database down")
}

type countingUploadStore struct {
	fakeUploadStore

	calls atomic.Int64
}

func (c *countingUploadStore) DeleteStaleDockerUploads(context.Context, time.Time) ([]string, error) {
	c.calls.Add(1)

	return nil, nil
}

// A database failure aborts the sweep before it touches the chunks: the rows
// are the source of truth for which sessions are gone.
func TestDockerUploadSweeperLeavesChunksWhenTheDatabaseFails(t *testing.T) {
	dir := t.TempDir()
	orphan := putChunk(t, dir, 72*time.Hour)

	NewDockerUploadSweeper(
		&errUploadStore{},
		storage.NewDirChunkStore(dir),
		time.Hour,
		testLogger(t),
	).Sweep(context.Background(), time.Now())

	if !chunkExists(dir, orphan) {
		t.Error("chunk removed although the session delete failed")
	}
}

// A missing chunk directory is not an error: nothing was ever uploaded.
func TestDockerUploadSweeperToleratesMissingDirectory(t *testing.T) {
	chunks := storage.NewDirChunkStore(filepath.Join(t.TempDir(), "absent"))

	NewDockerUploadSweeper(&fakeUploadStore{}, chunks, time.Hour, testLogger(t)).Sweep(context.Background(), time.Now())
}

// Start sweeps right away, a disabled sweeper (no chunk store or ttl) never
// touches the store, and Stop returns for both.
func TestDockerUploadSweeperStart(t *testing.T) {
	t.Run("sweeps at startup and stops", func(t *testing.T) {
		store := &countingUploadStore{}
		s := NewDockerUploadSweeper(store, storage.NewDirChunkStore(t.TempDir()), time.Hour, testLogger(t))

		s.Start()
		pollUntil(t, "the startup sweep", func() bool { return store.calls.Load() >= 1 })
		s.Stop()
	})

	for name, chunks := range map[string]storage.ChunkStore{
		"no chunk store": nil,
		"no ttl":         storage.NewDirChunkStore("x"),
	} {
		t.Run("disabled: "+name, func(t *testing.T) {
			store := &countingUploadStore{}
			ttl := time.Hour

			if name == "no ttl" {
				ttl = 0
			}

			s := NewDockerUploadSweeper(store, chunks, ttl, testLogger(t))

			s.Start()
			s.Stop()

			if store.calls.Load() != 0 {
				t.Error("a disabled sweeper queried the store")
			}
		})
	}
}
