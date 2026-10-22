// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

const dockerUploadSweepInterval = 15 * time.Minute

// DockerUploadSweeper deletes abandoned docker blob upload sessions: rows idle
// longer than the TTL and the staged files of cancelled or crashed pushes. A
// file counts as abandoned once its modification time is older than the TTL,
// whether or not a row still exists (repository deletes cascade the rows).
type DockerUploadSweeper struct {
	store database.DockerUploadSweepStore
	dir   string
	ttl   time.Duration
	log   *logger.Logger

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewDockerUploadSweeper builds the sweeper. A non-positive ttl, an empty dir
// or a database without DockerUploadSweepStore yields a sweeper that does
// nothing.
func NewDockerUploadSweeper(db database.Database, dir string, ttl time.Duration, log *logger.Logger) *DockerUploadSweeper {
	store, _ := db.(database.DockerUploadSweepStore)

	return &DockerUploadSweeper{
		store:  store,
		dir:    dir,
		ttl:    ttl,
		log:    log,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start begins the periodic sweep.
func (s *DockerUploadSweeper) Start() {
	s.startOnce.Do(s.start)
}

func (s *DockerUploadSweeper) start() {
	if s.ttl <= 0 || s.dir == "" || s.store == nil {
		close(s.doneCh)
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Errorw("panic in goroutine", "panic", r)
			}
		}()
		defer close(s.doneCh)

		ticker := time.NewTicker(dockerUploadSweepInterval)
		defer ticker.Stop()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			<-s.stopCh
			cancel()
		}()

		s.Sweep(ctx, time.Now())

		for {
			select {
			case <-ticker.C:
				s.Sweep(ctx, time.Now())
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop ends the sweep loop and waits for it to exit.
func (s *DockerUploadSweeper) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })

	// Never started: nothing will close doneCh, and a later Start must not.
	s.startOnce.Do(func() { close(s.doneCh) })

	<-s.doneCh
}

// Sweep deletes upload sessions and staged files idle since before now-ttl.
func (s *DockerUploadSweeper) Sweep(ctx context.Context, now time.Time) {
	cutoff := now.Add(-s.ttl)

	uuids, err := s.store.DeleteStaleDockerUploads(ctx, cutoff)
	if err != nil {
		s.log.Errorw("docker upload sweep: delete stale sessions", "error", err)
		return
	}

	for _, id := range uuids {
		// The id is a DB value that originated as a uuid; never join anything
		// else into a path.
		if _, perr := uuid.Parse(id); perr != nil {
			continue
		}

		if rerr := os.Remove(filepath.Join(s.dir, id)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			s.log.Warnw("docker upload sweep: remove staged file", "upload", id, "error", rerr)
		}
	}

	removed := len(uuids) + s.sweepOrphanFiles(cutoff)
	if removed > 0 {
		s.log.Infow("docker upload sweep", "removed", removed, "cutoff", cutoff)
	}
}

// sweepOrphanFiles removes staged files untouched since the cutoff. An active
// upload rewrites its file on every chunk, so an old modification time means
// the push is gone.
func (s *DockerUploadSweeper) sweepOrphanFiles(cutoff time.Time) int {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warnw("docker upload sweep: read upload dir", "error", err)
		}

		return 0
	}

	removed := 0

	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}

		if _, perr := uuid.Parse(e.Name()); perr != nil {
			continue
		}

		info, ierr := e.Info()
		if ierr != nil || !info.ModTime().Before(cutoff) {
			continue
		}

		if rerr := os.Remove(filepath.Join(s.dir, e.Name())); rerr == nil {
			removed++
		}
	}

	return removed
}
