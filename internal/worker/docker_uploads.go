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
// longer than the TTL (their chunk rows go with them) and the chunk objects no
// session lists any more, whatever left them behind (a cancelled or crashed
// push, a repository deleted with uploads in flight, a failed object delete).
// Every replica runs it: deleting a session is one atomic statement, deleting a
// chunk object is idempotent, and a replica that keeps chunks on its own disk
// (the oci backend) is the only one that can sweep them, so no lock elects a
// single sweeper.
type DockerUploadSweeper struct {
	store  database.DockerUploadSweepStore
	chunks storage.ChunkStore
	ttl    time.Duration
	log    *logger.Logger

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewDockerUploadSweeper builds the sweeper over the chunk store of the blob
// store (storage.ChunksFor). A non-positive ttl, a nil chunk store or a
// database without DockerUploadSweepStore yields a sweeper that does nothing.
func NewDockerUploadSweeper(db any, chunks storage.ChunkStore, ttl time.Duration, log *logger.Logger) *DockerUploadSweeper {
	store, _ := db.(database.DockerUploadSweepStore)

	return &DockerUploadSweeper{
		store:  store,
		chunks: chunks,
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
	if s.ttl <= 0 || s.chunks == nil || s.store == nil {
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

// Sweep deletes upload sessions idle since before now-ttl and the chunk objects
// untouched since then that no session lists.
func (s *DockerUploadSweeper) Sweep(ctx context.Context, now time.Time) {
	cutoff := now.Add(-s.ttl)

	uuids, err := s.store.DeleteStaleDockerUploads(ctx, cutoff)
	if err != nil {
		s.log.Errorw("docker upload sweep: delete stale sessions", "error", err)
		return
	}

	removed := len(uuids) + s.sweepOrphanChunks(ctx, cutoff)
	if removed > 0 {
		s.log.Infow("docker upload sweep", "removed", removed, "cutoff", cutoff)
	}
}

// sweepOrphanChunks deletes the chunk objects last modified before the cutoff
// that no live session lists. A chunk is written once, right before its row, so
// an old object without a row is garbage. It returns how many it deleted.
func (s *DockerUploadSweeper) sweepOrphanChunks(ctx context.Context, cutoff time.Time) int {
	removed := 0

	err := s.chunks.WalkChunks(ctx, cutoff, func(ids []string) error {
		known, err := s.store.ExistingDockerUploadChunkIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("look up chunk rows: %w", err)
		}

		listed := make(map[string]struct{}, len(known))
		for _, id := range known {
			listed[id] = struct{}{}
		}

		for _, id := range ids {
			if _, ok := listed[id]; ok {
				continue
			}

			if err := s.chunks.DeleteChunk(ctx, id); err != nil {
				s.log.Warnw("docker upload sweep: delete chunk", "chunk", id, "error", err)
				continue
			}

			removed++
		}

		return ctx.Err()
	})
	if err != nil {
		s.log.Warnw("docker upload sweep: walk chunks", "error", err)
	}

	return removed
}
