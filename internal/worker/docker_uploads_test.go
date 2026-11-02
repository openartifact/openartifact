// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/storage"
)

// fakeUploadStore is the sweeper's database: stale is what DeleteStaleDockerUploads
// returns, listed the ids of the chunk rows that exist.
type fakeUploadStore struct {
	stale     []string
	listed    map[string]bool
	got       time.Time
	lookupErr error
}

func (f *fakeUploadStore) DeleteStaleDockerUploads(_ context.Context, cutoff time.Time) ([]string, error) {
	f.got = cutoff

	return f.stale, nil
}

func (f *fakeUploadStore) ExistingDockerUploadChunkIDs(_ context.Context, ids []string) ([]string, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}

	var known []string

	for _, id := range ids {
		if f.listed[id] {
			known = append(known, id)
		}
	}

	return known, nil
}

// putChunk stores a chunk object in dir through the dir chunk store, last
// modified age ago.
func putChunk(t *testing.T, dir string, age time.Duration) string {
	t.Helper()

	id := uuid.NewString()
	if _, err := storage.NewDirChunkStore(dir).PutChunk(context.Background(), id, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}

	when := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(dir, id), when, when); err != nil {
		t.Fatal(err)
	}

	return id
}

func chunkExists(dir, id string) bool {
	_, err := os.Stat(filepath.Join(dir, id))

	return !errors.Is(err, os.ErrNotExist)
}

// FMTA-12: abandoned upload sessions lose their row; a chunk object older than
// the ttl that no session lists is removed, one a session lists and a fresh one
// are kept.
func TestDockerUploadSweeper_RemovesOrphanChunks(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	orphan := putChunk(t, dir, 48*time.Hour)
	listed := putChunk(t, dir, 48*time.Hour)
	fresh := putChunk(t, dir, time.Hour)

	store := &fakeUploadStore{stale: []string{uuid.NewString()}, listed: map[string]bool{listed: true}}
	s := NewDockerUploadSweeper(store, storage.NewDirChunkStore(dir), 24*time.Hour, testLogger(t))
	s.Sweep(context.Background(), now)

	if want := now.Add(-24 * time.Hour); !store.got.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", store.got, want)
	}

	for name, c := range map[string]struct {
		id         string
		wantExists bool
	}{"orphan": {orphan, false}, "listed": {listed, true}, "fresh": {fresh, true}} {
		if got := chunkExists(dir, c.id); got != c.wantExists {
			t.Errorf("%s chunk exists=%v, want %v", name, got, c.wantExists)
		}
	}
}

// A chunk right at the ttl boundary is kept until it is really older than it.
func TestDockerUploadSweeperKeepsChunksYoungerThanTTL(t *testing.T) {
	dir := t.TempDir()
	young, old := putChunk(t, dir, 23*time.Hour), putChunk(t, dir, 25*time.Hour)

	s := NewDockerUploadSweeper(&fakeUploadStore{}, storage.NewDirChunkStore(dir), 24*time.Hour, testLogger(t))
	s.Sweep(context.Background(), time.Now())

	if !chunkExists(dir, young) {
		t.Error("a chunk younger than the ttl was removed")
	}

	if chunkExists(dir, old) {
		t.Error("a chunk older than the ttl survived")
	}
}

// Foreign files in the directory are never touched: only uuid-named chunks are
// candidates.
func TestDockerUploadSweeperIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "notes.txt")

	if err := os.WriteFile(foreign, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatal(err)
	}

	s := NewDockerUploadSweeper(&fakeUploadStore{}, storage.NewDirChunkStore(dir), 24*time.Hour, testLogger(t))
	s.Sweep(context.Background(), time.Now())

	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign file removed: %v", err)
	}
}

// A failed row lookup deletes nothing: a chunk whose row could not be checked
// may belong to a live upload.
func TestDockerUploadSweeperKeepsChunksWhenTheRowLookupFails(t *testing.T) {
	dir := t.TempDir()
	orphan := putChunk(t, dir, 72*time.Hour)

	store := &fakeUploadStore{lookupErr: errors.New("database down")}
	NewDockerUploadSweeper(store, storage.NewDirChunkStore(dir), time.Hour, testLogger(t)).Sweep(context.Background(), time.Now())

	if !chunkExists(dir, orphan) {
		t.Error("chunk removed although the row lookup failed")
	}
}
