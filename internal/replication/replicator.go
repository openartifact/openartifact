// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/database"
)

const (
	// claimLease is how long a claimed operation is invisible to other
	// replicas. A crashed worker's operations come back after it; a healthy one
	// finishes (or releases) them long before.
	claimLease = 15 * time.Minute

	// statsEvery is how often the backlog gauges are refreshed.
	statsEvery = 15 * time.Second

	// stopGrace is how long Stop lets in-flight copies finish before it cancels them.
	stopGrace = 30 * time.Second

	// dbTimeout bounds the bookkeeping calls made after an operation ran, on a
	// context that survives the worker's cancellation.
	dbTimeout = 15 * time.Second
)

// Replicator works the replication queue off: it claims due operations,
// copies blobs to the replica and applies deletions to it, retrying failures
// with exponential backoff. Several replicas can run it: the queue hands every
// operation to one worker at a time.
type Replicator struct {
	*core

	kickCh chan struct{}

	// hadError is true while the stored last error may be stale; the first
	// error-free batch clears it.
	hadError  atomic.Bool
	lastStats time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	runCtx    context.Context //nolint:containedctx // lifecycle context of the worker
	cancel    context.CancelFunc
	grace     time.Duration
}

func newReplicator(c *core, kickCh chan struct{}) *Replicator {
	runCtx, cancel := context.WithCancel(context.Background())

	r := &Replicator{
		core: c, kickCh: kickCh,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
		runCtx: runCtx, cancel: cancel, grace: stopGrace,
	}
	r.hadError.Store(true)

	return r
}

// Start begins working the queue. Only the first call counts.
func (r *Replicator) Start() {
	r.startOnce.Do(func() { go r.loop() })
}

// Stop stops claiming, lets in-flight copies finish for a grace period, then
// cancels them (their operations stay queued and their claims expire) and waits
// for the loop to end. It is idempotent and safe without Start.
func (r *Replicator) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.startOnce.Do(func() { close(r.doneCh) })

	select {
	case <-r.doneCh:
	case <-time.After(r.grace):
		r.cancel()
		<-r.doneCh
	}

	r.cancel()
}

func (r *Replicator) stopped() bool {
	select {
	case <-r.stopCh:
		return true
	default:
		return false
	}
}

func (r *Replicator) loop() {
	defer close(r.doneCh)

	t := time.NewTicker(r.opts.Interval)
	defer t.Stop()

	r.cycle()

	for {
		select {
		case <-r.stopCh:
			return
		case <-t.C:
		case <-r.kickCh:
		}

		r.cycle()
	}
}

// cycle works the queue until nothing is due, then refreshes the gauges.
func (r *Replicator) cycle() {
	defer func() {
		if p := recover(); p != nil {
			r.log.Errorw("replication: worker panicked", "panic", p)
		}
	}()

	for !r.stopped() {
		n, err := r.RunOnce(r.runCtx)
		if err != nil {
			if r.runCtx.Err() == nil {
				r.log.Errorw("replication: pass failed", "error", err)
			}

			break
		}

		if n < r.opts.Batch {
			break
		}
	}

	if r.now().Sub(r.lastStats) >= statsEvery {
		r.lastStats = r.now()
		r.RefreshStats(r.runCtx)
	}
}

// RefreshStats updates the backlog gauges from the queue.
func (r *Replicator) RefreshStats(ctx context.Context) {
	b, err := r.queue.BlobReplicationBacklog(ctx, failingAttempts)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warnw("replication: could not read the backlog", "error", err)
		}

		return
	}

	r.m.Backlog.WithLabelValues(database.BlobReplicationCopy).Set(float64(b.Copy))
	r.m.Backlog.WithLabelValues(database.BlobReplicationDelete).Set(float64(b.Delete))
	r.m.OldestPendingAge.Set(b.OldestAgeSeconds)
}

// batchResult accumulates what one RunOnce did.
type batchResult struct {
	copies    atomic.Int64
	bytes     atomic.Int64
	successes atomic.Int64
	failures  atomic.Int64

	mu      sync.Mutex
	lastErr string
}

func (b *batchResult) fail(msg string) {
	b.failures.Add(1)
	b.mu.Lock()
	b.lastErr = msg
	b.mu.Unlock()
}

// RunOnce claims one batch of due operations and works them off with the
// configured concurrency. It returns how many it claimed.
func (r *Replicator) RunOnce(ctx context.Context) (int, error) {
	ops, err := r.queue.ClaimBlobReplications(ctx, r.now(), r.opts.Batch, claimLease)
	if err != nil {
		return 0, fmt.Errorf("claim replication batch: %w", err)
	}

	if len(ops) == 0 {
		return 0, nil
	}

	var (
		res  batchResult
		next atomic.Int64
		wg   sync.WaitGroup
	)

	for range min(r.opts.Concurrency, len(ops)) {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= len(ops) {
					return
				}

				if r.stopped() || ctx.Err() != nil {
					r.release(ops[i])

					continue
				}

				r.handle(ctx, ops[i], &res)
			}
		})
	}

	wg.Wait()
	r.bookkeeping(&res)

	return len(ops), nil
}

// release gives an unprocessed claim back.
func (r *Replicator) release(op database.BlobReplication) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	if err := r.queue.ReleaseBlobReplication(ctx, op.SHA256, op.Version); err != nil {
		r.log.Warnw("replication: could not release a claim; it expires by itself", "sha256", op.SHA256, "error", err)
	}
}

// bookkeeping stores the batch's totals and last error.
func (r *Replicator) bookkeeping(res *batchResult) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	if n := res.copies.Load(); n > 0 {
		if err := r.queue.AddBlobReplicationProgress(ctx, n, res.bytes.Load()); err != nil {
			r.log.Warnw("replication: could not store progress", "error", err)
		}
	}

	switch {
	case res.failures.Load() > 0:
		res.mu.Lock()
		msg := res.lastErr
		res.mu.Unlock()

		if err := r.queue.SetBlobReplicationLastError(ctx, msg); err != nil {
			r.log.Warnw("replication: could not store the last error", "error", err)
		} else {
			r.hadError.Store(true)
		}
	case res.successes.Load() > 0 && r.hadError.Load():
		if err := r.queue.SetBlobReplicationLastError(ctx, ""); err == nil {
			r.hadError.Store(false)
		}
	}
}

// handle works one claimed operation off and records its outcome.
func (r *Replicator) handle(ctx context.Context, op database.BlobReplication, res *batchResult) {
	var (
		n   int64
		err error
	)

	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p) //nolint:err113 // converted panic
			}
		}()

		switch op.Op {
		case database.BlobReplicationCopy:
			n, err = r.doCopy(ctx, op)
		case database.BlobReplicationDelete:
			err = r.doDelete(ctx, op)
		default:
			err = fmt.Errorf("unknown replication op %q", op.Op) //nolint:err113 // cannot happen: the column is constrained
		}
	}()

	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	defer cancel()

	if err == nil {
		r.complete(dbCtx, op, n, res)

		return
	}

	if ctx.Err() != nil {
		r.release(op)

		return
	}

	r.m.Failures.WithLabelValues(op.Op).Inc()
	res.fail(truncateError(err))

	delay := r.backoff(op.Attempts)
	if _, ferr := r.queue.FailBlobReplication(dbCtx, op.SHA256, op.Version, truncateError(err), r.now().Add(delay)); ferr != nil {
		r.log.Warnw("replication: could not record a failed attempt", "sha256", op.SHA256, "error", ferr)
	}

	r.log.Warnw("replication: operation failed, will retry", "op", op.Op, "sha256", op.SHA256, "attempts", op.Attempts+1, "retry_in", delay.String(), "error", err)
}

func (r *Replicator) complete(ctx context.Context, op database.BlobReplication, n int64, res *batchResult) {
	if _, err := r.queue.CompleteBlobReplication(ctx, op.SHA256, op.Version); err != nil {
		r.log.Warnw("replication: could not complete an operation; it will run again", "sha256", op.SHA256, "error", err)

		return
	}

	res.successes.Add(1)

	if op.Op == database.BlobReplicationCopy && n > 0 {
		res.copies.Add(1)
		res.bytes.Add(n)
		r.m.Replicated.Inc()
		r.m.ReplicatedBytes.Add(float64(n))
	}
}

// doCopy makes the blob exist on the replica. A blob that is gone from the
// primary and unreferenced was deleted after it was queued (its deletion
// would replace this operation, so this is a leftover): the operation is
// dropped. A blob the database still references but the primary lacks fails
// with ErrMissingPrimary until the verify pass or a read repairs the primary.
func (r *Replicator) doCopy(ctx context.Context, op database.BlobReplication) (int64, error) {
	n, err := r.copyToReplica(ctx, op.SHA256)
	if !errors.Is(err, ErrMissingPrimary) {
		return n, err
	}

	r.m.MissingPrimary.Inc()

	referenced, rerr := r.queue.BlobReplicationReferenced(ctx, op.SHA256)
	if rerr != nil {
		return 0, rerr //nolint:wrapcheck // wrapped by the database layer
	}

	if !referenced {
		r.log.Infow("replication: dropping the copy of a blob that no longer exists", "sha256", op.SHA256)

		return 0, nil
	}

	return 0, err
}

// doDelete removes the blob's replica copy. Under the per-blob lock it first
// makes sure the deletion still stands: the operation may have been replaced
// by a newer copy, and a blob that is on the primary again or still referenced
// must keep its replica copy (the operation then turns into a copy).
func (r *Replicator) doDelete(ctx context.Context, op database.BlobReplication) error {
	return r.withLock(ctx, op.SHA256, func(ctx context.Context) error {
		cur, err := r.pending(ctx, op.SHA256)
		if err != nil {
			return err
		}

		if cur == nil || cur.Op != database.BlobReplicationDelete || cur.Version != op.Version {
			return nil // superseded: the newer operation decides
		}

		exists, err := r.inner.Exists(ctx, op.SHA256)
		if err != nil {
			return fmt.Errorf("check blob %s on the primary store: %w", op.SHA256, err)
		}

		referenced := exists

		if !referenced {
			referenced, err = r.queue.BlobReplicationReferenced(ctx, op.SHA256)
			if err != nil {
				return err //nolint:wrapcheck // wrapped by the database layer
			}
		}

		if referenced {
			r.log.Warnw("replication: not deleting the replica copy of a blob that is still in use", "sha256", op.SHA256)

			return r.enqueue(ctx, op.SHA256, database.BlobReplicationCopy)
		}

		if err := r.replica.Delete(ctx, op.SHA256); err != nil && !isNotFound(err) {
			return fmt.Errorf("delete blob %s from the replica: %w", op.SHA256, err)
		}

		return nil
	})
}
