// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// repairQueueSize bounds the blobs waiting for a read repair. When it is
	// full new ones are dropped: the verify pass repairs them later.
	repairQueueSize = 1024

	// repairWorkers is how many blobs are written back to the primary at once.
	repairWorkers = 2

	// repairRetryAfter is how long a blob that was just repaired (or failed to
	// be) is not scheduled again, so a burst of reads of one missing blob does
	// one repair.
	repairRetryAfter = time.Minute

	// repairSeenLimit bounds the memory of recently scheduled blobs.
	repairSeenLimit = 8192
)

// repairer writes blobs the primary lost back from the replica after a read
// was served from it. Scheduling is coalesced and never blocks the read.
type repairer struct {
	c  *core
	ch chan string

	mu   sync.Mutex
	seen map[string]time.Time
}

func newRepairer(c *core) *repairer {
	return &repairer{c: c, ch: make(chan string, repairQueueSize), seen: make(map[string]time.Time)}
}

// schedule queues the blob for repair unless it was scheduled recently or the
// queue is full.
func (r *repairer) schedule(id string) {
	now := r.c.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	if at, ok := r.seen[id]; ok && now.Sub(at) < repairRetryAfter {
		return
	}

	if len(r.seen) >= repairSeenLimit {
		for k, at := range r.seen {
			if now.Sub(at) >= repairRetryAfter {
				delete(r.seen, k)
			}
		}

		if len(r.seen) >= repairSeenLimit {
			return
		}
	}

	select {
	case r.ch <- id:
		r.seen[id] = now
	default:
	}
}

// run repairs queued blobs until ctx ends.
func (r *repairer) run(ctx context.Context, wg *sync.WaitGroup) {
	for range repairWorkers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-r.ch:
					r.repair(ctx, id)
				}
			}
		})
	}
}

func (r *repairer) repair(ctx context.Context, id string) {
	defer func() {
		if p := recover(); p != nil {
			r.c.log.Errorw("replication: read repair panicked", "sha256", id, "panic", p)
		}
	}()

	restored, err := r.c.restorePrimary(ctx, id, false)

	switch {
	case err == nil && restored:
		r.c.m.VerifyRepaired.WithLabelValues("primary").Inc()
		r.c.log.Infow("blob restored on the primary store from the replica", "sha256", id)
	case err == nil, errors.Is(err, errPendingDelete):
	default:
		r.c.log.Warnw("could not restore blob on the primary store; the verify pass will retry", "sha256", id, "error", err)
	}
}
