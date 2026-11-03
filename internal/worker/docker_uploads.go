// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const dockerUploadSweepInterval = 15 * time.Minute

// DockerUploadSweeper deletes abandoned docker blob upload sessions: rows idle
// longer than the TTL together with the data they hold (a multipart upload in
// progress is aborted, which is what frees its parts), and the upload data no
// session owns any more, whatever left it behind (a cancelled or crashed push,
// a repository deleted with uploads in flight, a failed delete). Every replica
// runs it: deleting a session is one atomic statement, deleting upload data is
// idempotent, and a replica that keeps uploads on its own disk (the oci
// backend) is the only one that can sweep them, so no lock elects a single
// sweeper.
type DockerUploadSweeper struct {
	store   database.DockerUploadSweepStore
	uploads storage.UploadStore
	ttl     time.Duration
	log     *logger.Logger

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewDockerUploadSweeper builds the sweeper over the upload store of the blob
// store (storage.UploadsFor). A non-positive ttl, a nil upload store or a
// database without DockerUploadSweepStore yields a sweeper that does nothing.
func NewDockerUploadSweeper(db any, uploads storage.UploadStore, ttl time.Duration, log *logger.Logger) *DockerUploadSweeper {
	store, _ := db.(database.DockerUploadSweepStore)

	return &DockerUploadSweeper{
		store:   store,
		uploads: uploads,
		ttl:     ttl,
		log:     log,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// Start begins the periodic sweep.
func (s *DockerUploadSweeper) Start() {
	s.startOnce.Do(s.start)
}

func (s *DockerUploadSweeper) start() {
	if s.ttl <= 0 || s.uploads == nil || s.store == nil {
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

// Sweep deletes upload sessions idle since before now-ttl with their data, and
// the upload data untouched since then that no session owns.
func (s *DockerUploadSweeper) Sweep(ctx context.Context, now time.Time) {
	cutoff := now.Add(-s.ttl)

	uuids, err := s.store.DeleteStaleDockerUploads(ctx, cutoff)
	if err != nil {
		s.log.Errorw("docker upload sweep: delete stale sessions", "error", err)
		return
	}

	for _, id := range uuids {
		if err := s.uploads.DeleteUpload(ctx, id); err != nil {
			s.log.Warnw("docker upload sweep: delete upload", "upload", id, "error", err)
		}
	}

	removed := len(uuids) + s.sweepOrphanUploads(ctx, cutoff)
	if removed > 0 {
		s.log.Infow("docker upload sweep", "removed", removed, "cutoff", cutoff)
	}
}

// sweepOrphanUploads deletes the uploads (objects and unfinished multipart
// uploads) started before the cutoff that no live session owns. An upload's
// data is created after its session row, so old data without a row is garbage.
// It returns how many uploads it deleted.
func (s *DockerUploadSweeper) sweepOrphanUploads(ctx context.Context, cutoff time.Time) int {
	removed := 0

	err := s.uploads.WalkUploads(ctx, cutoff, func(ids []string) error {
		known, err := s.store.ExistingDockerUploadIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("look up upload sessions: %w", err)
		}

		owned := make(map[string]struct{}, len(known))
		for _, id := range known {
			owned[id] = struct{}{}
		}

		for _, id := range ids {
			if _, ok := owned[id]; ok {
				continue
			}

			if err := s.uploads.DeleteUpload(ctx, id); err != nil {
				s.log.Warnw("docker upload sweep: delete upload", "upload", id, "error", err)
				continue
			}

			removed++
		}

		return ctx.Err()
	})
	if err != nil {
		s.log.Warnw("docker upload sweep: walk uploads", "error", err)
	}

	return removed
}
