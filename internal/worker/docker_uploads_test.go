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
// returns, listed the ids of the sessions that exist.
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

func (f *fakeUploadStore) ExistingDockerUploadIDs(_ context.Context, ids []string) ([]string, error) {
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

// putUpload stores an upload with a byte of data in dir through the dir upload
// store, last modified age ago.
func putUpload(t *testing.T, dir string, age time.Duration) string {
	t.Helper()

	id := uuid.NewString()

	err := storage.NewDirUploadStore(dir).AppendUpload(context.Background(), id, storage.UploadPosition{}, strings.NewReader("x"),
		func(context.Context, string, int64) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	when := time.Now().Add(-age)
	if err := os.Chtimes(filepath.Join(dir, id), when, when); err != nil {
		t.Fatal(err)
	}

	return id
}

func uploadExists(dir, id string) bool {
	_, err := os.Stat(filepath.Join(dir, id))

	return !errors.Is(err, os.ErrNotExist)
}

// FMTA-12: abandoned upload sessions lose their row and their data; an upload
// older than the ttl that no session owns is removed, one a session owns and a
// fresh one are kept.
func TestDockerUploadSweeper_RemovesOrphanUploads(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	orphan := putUpload(t, dir, 48*time.Hour)
	owned := putUpload(t, dir, 48*time.Hour)
	fresh := putUpload(t, dir, time.Hour)
	abandoned := putUpload(t, dir, time.Hour) // its session row is stale: the data goes with it

	store := &fakeUploadStore{stale: []string{abandoned}, listed: map[string]bool{owned: true}}
	s := NewDockerUploadSweeper(store, storage.NewDirUploadStore(dir), 24*time.Hour, testLogger(t))
	s.Sweep(context.Background(), now)

	if want := now.Add(-24 * time.Hour); !store.got.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", store.got, want)
	}

	for name, c := range map[string]struct {
		id         string
		wantExists bool
	}{"orphan": {orphan, false}, "owned": {owned, true}, "fresh": {fresh, true}, "abandoned": {abandoned, false}} {
		if got := uploadExists(dir, c.id); got != c.wantExists {
			t.Errorf("%s upload exists=%v, want %v", name, got, c.wantExists)
		}
	}
}

// An upload right at the ttl boundary is kept until it is really older than it.
func TestDockerUploadSweeperKeepsUploadsYoungerThanTTL(t *testing.T) {
	dir := t.TempDir()
	young, old := putUpload(t, dir, 23*time.Hour), putUpload(t, dir, 25*time.Hour)

	s := NewDockerUploadSweeper(&fakeUploadStore{}, storage.NewDirUploadStore(dir), 24*time.Hour, testLogger(t))
	s.Sweep(context.Background(), time.Now())

	if !uploadExists(dir, young) {
		t.Error("an upload younger than the ttl was removed")
	}

	if uploadExists(dir, old) {
		t.Error("an upload older than the ttl survived")
	}
}

// Foreign files in the directory are never touched: only uuid-named uploads are
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

	s := NewDockerUploadSweeper(&fakeUploadStore{}, storage.NewDirUploadStore(dir), 24*time.Hour, testLogger(t))
	s.Sweep(context.Background(), time.Now())

	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("foreign file removed: %v", err)
	}
}

// A failed session lookup deletes nothing: an upload whose session could not be
// checked may belong to a live push.
func TestDockerUploadSweeperKeepsUploadsWhenTheSessionLookupFails(t *testing.T) {
	dir := t.TempDir()
	orphan := putUpload(t, dir, 72*time.Hour)

	store := &fakeUploadStore{lookupErr: errors.New("database down")}
	NewDockerUploadSweeper(store, storage.NewDirUploadStore(dir), time.Hour, testLogger(t)).Sweep(context.Background(), time.Now())

	if !uploadExists(dir, orphan) {
		t.Error("upload removed although the session lookup failed")
	}
}
