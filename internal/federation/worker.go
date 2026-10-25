// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"sync"
	"time"
)

const (
	// maxConcurrentPulls bounds the peers pulled at once by one replica.
	maxConcurrentPulls = 8
	// gcInterval is how often expired tombstones are purged.
	gcInterval = time.Hour
	// sweepInterval is how often the download spool is trimmed to its caps
	// (the first pass of a worker, at startup, included).
	sweepInterval = 10 * time.Minute
	// schedulerTick is the longest wait between looks for due peers.
	schedulerTick = time.Second
)

// Worker runs the pull scheduler of an Engine.
type Worker struct {
	e      *Engine
	cancel context.CancelFunc
	done   chan struct{}
}

// StartWorker launches the scheduler; Stop ends it and waits for the pulls in
// flight (they are cancelled, the cursor of what was applied is kept).
func (e *Engine) StartWorker(ctx context.Context) *Worker {
	ctx, cancel := context.WithCancel(ctx)
	w := &Worker{e: e, cancel: cancel, done: make(chan struct{})}

	go func() {
		defer close(w.done)

		e.run(ctx)
	}()

	return w
}

// Stop cancels the scheduler and waits until it has finished.
func (w *Worker) Stop() {
	w.cancel()

	<-w.done
}

// scheduler is the state of one run of the pull scheduler.
type scheduler struct {
	e  *Engine
	wg sync.WaitGroup

	mu       sync.Mutex
	inflight map[string]bool
	// sem bounds the pulls in flight (maxConcurrentPulls).
	sem       chan struct{}
	lastGC    time.Time
	lastSweep time.Time
}

func (e *Engine) run(ctx context.Context) {
	s := &scheduler{e: e, inflight: map[string]bool{}, sem: make(chan struct{}, maxConcurrentPulls)}

	defer s.wg.Wait()

	tick := time.NewTicker(min(e.PollInterval, schedulerTick))
	defer tick.Stop()

	for {
		s.pass(ctx)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.wake:
		}
	}
}

// pass starts a pull for every due peer that has no pull in flight and a free
// slot, and purges expired tombstones now and then. A panic ends this pass
// only: the scheduler keeps running.
func (s *scheduler) pass(ctx context.Context) {
	e := s.e

	defer func() {
		if r := recover(); r != nil {
			e.logPanic("scheduler", r)
		}
	}()

	peers, err := e.Store.DuePeers(ctx, e.PollInterval)
	if err != nil && ctx.Err() == nil {
		e.Log.Warnw("federation: list due peers", "error", err)
	}

	for _, p := range peers {
		if !s.claim(p.ID) {
			continue
		}

		s.wg.Go(func() {
			defer s.release(p.ID)

			// PullPeer turns a panic inside the pull into a recorded peer
			// error; this catches the rest (locking, bookkeeping) so the
			// slot is released and the process survives.
			defer func() {
				if r := recover(); r != nil {
					e.logPanic("pull worker", r, "peer_id", p.ID)
				}
			}()

			_ = e.PullPeer(ctx, p.ID, p.RepoID) // failures are recorded on the peer
		})
	}

	if time.Since(s.lastGC) >= gcInterval {
		s.lastGC = time.Now()

		e.purgeTombstones(ctx)
	}

	if time.Since(s.lastSweep) >= sweepInterval {
		s.lastSweep = time.Now()

		if n := e.spool.sweep(time.Now()); n > 0 {
			e.Log.Infow("federation: removed expired partial downloads", "count", n)
		}
	}
}

// claim reserves a pull slot for the peer; false when it is already being
// pulled or all slots are taken.
func (s *scheduler) claim(peerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.inflight[peerID] {
		return false
	}

	select {
	case s.sem <- struct{}{}:
		s.inflight[peerID] = true

		return true
	default:
		return false
	}
}

func (s *scheduler) release(peerID string) {
	s.mu.Lock()
	delete(s.inflight, peerID)
	s.mu.Unlock()

	<-s.sem
}

func (e *Engine) purgeTombstones(ctx context.Context) {
	release, locked, err := tryLock(ctx, e.SQL, "federation:gc")
	if err != nil || !locked {
		return
	}
	defer release()

	n, err := e.Store.PurgeTombstones(ctx, e.TombstoneRetention)
	if err != nil {
		e.Log.Warnw("federation: purge tombstones", "error", err)

		return
	}

	if n > 0 {
		e.Log.Infow("federation: purged expired tombstones", "count", n)
	}
}
