// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type errUploadStore struct{}

func (errUploadStore) DeleteStaleDockerUploads(context.Context, time.Time) ([]string, error) {
	return nil, errors.New("database down")
}

type countingUploadStore struct{ calls atomic.Int64 }

func (c *countingUploadStore) DeleteStaleDockerUploads(context.Context, time.Time) ([]string, error) {
	c.calls.Add(1)

	return nil, nil
}

func writeAged(t *testing.T, path string, age time.Duration) {
	t.Helper()

	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// The sweeper only ever removes uuid-named regular files inside its own
// directory: a hostile id from the database, foreign files and directories stay.
func TestDockerUploadSweeperStaysInsideItsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "uploads")

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(root, "precious")
	writeAged(t, outside, 72*time.Hour)

	foreign := filepath.Join(dir, "notes.txt")
	writeAged(t, foreign, 72*time.Hour)

	subdirName := uuid.NewString()
	if err := os.Mkdir(filepath.Join(dir, subdirName), 0o700); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(dir, uuid.NewString())
	writeAged(t, stale, 72*time.Hour)

	store := &fakeUploadStore{stale: []string{"../precious", "..", "", "not-a-uuid"}}
	s := &DockerUploadSweeper{store: store, dir: dir, ttl: 24 * time.Hour, log: testLogger(t)}

	s.Sweep(context.Background(), time.Now())

	for name, path := range map[string]string{"file outside the directory": outside, "foreign file": foreign, "uuid-named directory": filepath.Join(dir, subdirName)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the abandoned staged file survived: %v", err)
	}
}

// A file right at the ttl boundary is kept until it is really older than it.
func TestDockerUploadSweeperKeepsFilesYoungerThanTTL(t *testing.T) {
	dir := t.TempDir()

	young, old := filepath.Join(dir, uuid.NewString()), filepath.Join(dir, uuid.NewString())
	writeAged(t, young, 23*time.Hour)
	writeAged(t, old, 25*time.Hour)

	s := &DockerUploadSweeper{store: &fakeUploadStore{}, dir: dir, ttl: 24 * time.Hour, log: testLogger(t)}
	s.Sweep(context.Background(), time.Now())

	if _, err := os.Stat(young); err != nil {
		t.Errorf("a file younger than the ttl was removed: %v", err)
	}

	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file older than the ttl survived: %v", err)
	}
}

// A database failure aborts the sweep before it touches the disk: the rows are
// the source of truth for which sessions are gone.
func TestDockerUploadSweeperLeavesFilesWhenTheDatabaseFails(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, uuid.NewString())
	writeAged(t, stale, 72*time.Hour)

	s := &DockerUploadSweeper{store: errUploadStore{}, dir: dir, ttl: time.Hour, log: testLogger(t)}
	s.Sweep(context.Background(), time.Now())

	if _, err := os.Stat(stale); err != nil {
		t.Errorf("file removed although the session lookup failed: %v", err)
	}
}

// A missing upload directory is not an error: nothing was ever staged.
func TestDockerUploadSweeperToleratesMissingDirectory(t *testing.T) {
	s := &DockerUploadSweeper{store: &fakeUploadStore{}, dir: filepath.Join(t.TempDir(), "absent"), ttl: time.Hour, log: testLogger(t)}

	s.Sweep(context.Background(), time.Now())
}

// Start sweeps right away, a disabled sweeper (no dir or ttl) never touches the
// store, and Stop returns for both.
func TestDockerUploadSweeperStart(t *testing.T) {
	t.Run("sweeps at startup and stops", func(t *testing.T) {
		store := &countingUploadStore{}
		s := &DockerUploadSweeper{store: store, dir: t.TempDir(), ttl: time.Hour, log: testLogger(t),
			stopCh: make(chan struct{}), doneCh: make(chan struct{})}

		s.Start()
		pollUntil(t, "the startup sweep", func() bool { return store.calls.Load() >= 1 })
		s.Stop()
	})

	for name, s := range map[string]*DockerUploadSweeper{
		"no directory": {ttl: time.Hour},
		"no ttl":       {dir: "x"},
	} {
		t.Run("disabled: "+name, func(t *testing.T) {
			store := &countingUploadStore{}
			s.store, s.log = store, testLogger(t)
			s.stopCh, s.doneCh = make(chan struct{}), make(chan struct{})

			s.Start()
			s.Stop()

			if store.calls.Load() != 0 {
				t.Error("a disabled sweeper queried the store")
			}
		})
	}
}
