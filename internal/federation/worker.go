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

func (e *Engine) run(ctx context.Context) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		inflight = map[string]bool{}
		sem      = make(chan struct{}, maxConcurrentPulls)
	)

	defer wg.Wait()

	tick := time.NewTicker(min(e.PollInterval, schedulerTick))
	defer tick.Stop()

	lastGC := time.Time{}

	for {
		peers, err := e.Store.DuePeers(ctx, e.PollInterval)
		if err != nil && ctx.Err() == nil {
			e.Log.Warnw("federation: list due peers", "error", err)
		}

		for _, p := range peers {
			mu.Lock()
			busy := inflight[p.ID]

			if !busy {
				select {
				case sem <- struct{}{}:
					inflight[p.ID] = true
				default:
					busy = true
				}
			}
			mu.Unlock()

			if busy {
				continue
			}

			wg.Go(func() {
				_ = e.PullPeer(ctx, p.ID, p.RepoID) // failures are recorded on the peer

				mu.Lock()
				delete(inflight, p.ID)
				mu.Unlock()

				<-sem
			})
		}

		if time.Since(lastGC) >= gcInterval {
			lastGC = time.Now()

			e.purgeTombstones(ctx)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.wake:
		}
	}
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
