// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// blobSweepClaimFor is how long a claimed deletion belongs to the sweeper
	// that took it. It is also the retry delay of a deletion that failed or was
	// held back at the last moment, and how long a crashed sweeper's rows wait.
	blobSweepClaimFor = 10 * time.Minute

	// blobSweepRunTimeout bounds one scheduled pass; the next one continues.
	blobSweepRunTimeout = time.Hour

	blobSweepResultDeleted = "deleted"
	blobSweepResultKept    = "kept"
)

// BlobByteStore removes blob bytes. *tiering.Store implements it.
type BlobByteStore interface {
	// Delete removes every copy of the blob, on every tier.
	Delete(ctx context.Context, sha256 string) error
	// DeleteCopy removes the copy on one backend ("" = the primary store).
	DeleteCopy(ctx context.Context, backendID, sha256 string) error
}

// placementReader tells where a blob lives; *database.PostgresDB implements it.
type placementReader interface {
	GetBlobPlacement(ctx context.Context, sha256 string) (backendID string, size int64, found bool, err error)
}

// BlobDeletionStats is the outcome of one sweep pass.
type BlobDeletionStats struct {
	// Deleted counts queued deletions whose bytes were removed.
	Deleted int
	// Kept counts queued deletions dropped without touching the bytes because
	// the blob was re-uploaded, or the copy became authoritative again.
	Kept int
	// Held counts deletions a running backup (or a concurrent newer deletion)
	// still holds back; they stay queued.
	Held int
	// Failed counts deletions whose byte removal failed; they stay queued and
	// are retried after the claim period.
	Failed int
	// Pending is the queue length after the pass.
	Pending int64
}

// BlobDeletionSweeper removes the bytes of deleted blobs and retired tier
// copies that SafeDeleteBlob and the tier mover queued (database.BlobDeletion).
//
// A deletion is carried out only when its grace period has passed and no
// running backup can still read the bytes (the database checks the backups'
// snapshot leases). Right before deleting, the sweeper takes the per-blob lock
// that uploaders and deleters share and checks again that the deletion is still
// due and that the bytes are not needed after all: a blob that was re-uploaded
// during the grace period (a blobs row exists again), or a copy that became the
// blob's placement again, is kept and the entry dropped. A copy that is already
// gone counts as deleted.
//
// There is no global lock: every replica may run a sweeper, rows are claimed
// with FOR UPDATE SKIP LOCKED and held for a claim period, and the per-blob lock
// serializes the actual deletion.
type BlobDeletionSweeper struct {
	db        database.Database
	queue     database.BlobDeletionStore
	placement placementReader
	store     BlobByteStore
	log       *logger.Logger
	interval  time.Duration
	batch     int
	reg       *metrics.Registry

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	// runCtx is cancelled by Stop so an in-flight pass stops promptly.
	runCtx context.Context //nolint:containedctx // lifecycle context of the worker
	cancel context.CancelFunc
}

// NewBlobDeletionSweeper creates the sweeper. interval is the pause between
// scheduled passes (a non-positive interval disables the schedule; RunOnce still
// works) and batch the number of deletions claimed at a time. Without a
// database that implements database.BlobDeletionStore the sweeper does nothing.
func NewBlobDeletionSweeper(db database.Database, store BlobByteStore, log *logger.Logger, interval time.Duration, batch int) *BlobDeletionSweeper {
	runCtx, cancel := context.WithCancel(context.Background())

	queue, _ := db.(database.BlobDeletionStore)
	placement, _ := db.(placementReader)

	return &BlobDeletionSweeper{
		db: db, queue: queue, placement: placement, store: store, log: log,
		interval: interval, batch: max(batch, 1),
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
		runCtx: runCtx, cancel: cancel,
	}
}

// WithMetrics publishes the queue length and the swept counts.
func (w *BlobDeletionSweeper) WithMetrics(reg *metrics.Registry) *BlobDeletionSweeper {
	w.reg = reg

	return w
}

// Start begins the scheduled passes. It does nothing when the interval is not
// positive or the database has no deletion queue. Only the first call counts.
func (w *BlobDeletionSweeper) Start() {
	w.startOnce.Do(func() {
		if w.interval <= 0 || w.queue == nil {
			close(w.doneCh)

			return
		}

		go w.loop()
	})
}

// Stop cancels an in-flight pass and waits for the schedule goroutine to exit.
// It is idempotent and safe without Start.
func (w *BlobDeletionSweeper) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.cancel()
	})

	// Never started: nothing will close doneCh, and a later Start must not.
	w.startOnce.Do(func() { close(w.doneCh) })

	<-w.doneCh
}

func (w *BlobDeletionSweeper) loop() {
	defer close(w.doneCh)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.runScheduled()
		case <-w.stopCh:
			return
		}
	}
}

// runScheduled runs one pass of the schedule. A panic ends the pass, not the
// schedule.
func (w *BlobDeletionSweeper) runScheduled() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in blob deletion sweep", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.runCtx, blobSweepRunTimeout)
	defer cancel()

	stats, err := w.RunOnce(ctx)
	if err != nil && w.runCtx.Err() == nil {
		w.log.Errorw("blob deletion sweep failed", "error", err)
	}

	if stats.Deleted > 0 || stats.Kept > 0 || stats.Failed > 0 {
		w.log.Infow("blob deletion sweep finished",
			"deleted", stats.Deleted, "kept", stats.Kept, "held", stats.Held, "failed", stats.Failed, "pending", stats.Pending)
	}
}

// RunOnce runs one pass: it claims due deletions batch by batch until none is
// left (or ctx ends) and processes each under its blob lock. A failing deletion
// is logged, counted in Failed and left queued; it never aborts the pass. The
// error is non-nil only when the queue could not be read or ctx ended.
func (w *BlobDeletionSweeper) RunOnce(ctx context.Context) (BlobDeletionStats, error) {
	var stats BlobDeletionStats

	if w.queue == nil {
		return stats, nil
	}

	// Leases of crashed backups already block nothing once expired; this only
	// keeps the table small. Best effort.
	if _, err := w.queue.PurgeExpiredBackupLeases(ctx); err != nil && ctx.Err() == nil {
		w.log.Warnw("failed to purge expired backup leases", "error", err)
	}

	err := w.drain(ctx, &stats)

	pending, cerr := w.queue.CountBlobDeletions(context.WithoutCancel(ctx))
	if cerr == nil {
		stats.Pending = pending

		if w.reg != nil {
			w.reg.BlobDeletionsPending.Set(float64(pending))
		}
	}

	return stats, err
}

func (w *BlobDeletionSweeper) drain(ctx context.Context, stats *BlobDeletionStats) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("blob deletion sweep: %w", err)
		}

		claimed, err := w.queue.ClaimBlobDeletions(ctx, w.batch, blobSweepClaimFor)
		if err != nil {
			return fmt.Errorf("claim blob deletions: %w", err)
		}

		if len(claimed) == 0 {
			return nil
		}

		for _, d := range claimed {
			w.process(ctx, d, stats)
		}
	}
}

// outcome of processing one queued deletion.
type sweepOutcome int

const (
	sweepDeleted sweepOutcome = iota
	sweepKept
	sweepHeld
)

func (w *BlobDeletionSweeper) process(ctx context.Context, d database.BlobDeletion, stats *BlobDeletionStats) {
	var out sweepOutcome

	err := storage.WithBlobLock(ctx, w.db, d.SHA256, func(ctx context.Context) error {
		var lerr error

		out, lerr = w.sweepLocked(ctx, d)

		return lerr
	})
	if err != nil {
		stats.Failed++

		if ctx.Err() == nil {
			w.log.Warnw("blob deletion failed; will retry", "sha256", d.SHA256, "scope", d.Scope, "attempts", d.Attempts, "error", err)
		}

		return
	}

	switch out {
	case sweepDeleted:
		stats.Deleted++
		w.count(blobSweepResultDeleted)
	case sweepKept:
		stats.Kept++
		w.count(blobSweepResultKept)
	case sweepHeld:
		stats.Held++
	}
}

func (w *BlobDeletionSweeper) count(result string) {
	if w.reg != nil {
		w.reg.BlobDeletionsSwept.WithLabelValues(result).Inc()
	}
}

// sweepLocked decides and acts on one deletion while holding the blob lock.
func (w *BlobDeletionSweeper) sweepLocked(ctx context.Context, d database.BlobDeletion) (sweepOutcome, error) {
	// The blob may have been deleted again, by a transaction a backup that
	// started after this one was claimed still sees, since the row was claimed.
	due, err := w.queue.BlobDeletionDue(ctx, d.SHA256, d.Scope)
	if err != nil {
		return sweepHeld, err //nolint:wrapcheck // already wrapped by the database layer
	}

	if !due {
		return sweepHeld, nil
	}

	needed, err := w.bytesNeeded(ctx, d)
	if err != nil {
		return sweepHeld, err
	}

	if needed {
		return sweepKept, w.drop(ctx, d)
	}

	if err := w.removeBytes(ctx, d); err != nil {
		return sweepHeld, err
	}

	return sweepDeleted, w.drop(ctx, d)
}

func (w *BlobDeletionSweeper) drop(ctx context.Context, d database.BlobDeletion) error {
	return w.queue.DeleteBlobDeletion(ctx, d.SHA256, d.Scope) //nolint:wrapcheck // already wrapped by the database layer
}

// bytesNeeded reports whether the blob is live again (re-uploaded during the
// grace period) or, for a single retired copy, authoritative again.
func (w *BlobDeletionSweeper) bytesNeeded(ctx context.Context, d database.BlobDeletion) (bool, error) {
	if d.Scope == database.BlobDeletionScopeAll {
		// Generated metadata and deltas reference blob bytes by sha256 with no
		// blobs row; whatever points at the bytes again keeps them.
		refs, err := w.db.BlobReferenceCount(ctx, d.SHA256)
		if err != nil {
			return false, fmt.Errorf("count blob references: %w", err)
		}

		if refs > 0 {
			return true, nil
		}
	}

	if w.placement == nil {
		return false, nil
	}

	backend, _, found, err := w.placement.GetBlobPlacement(ctx, d.SHA256)
	if err != nil {
		return false, fmt.Errorf("read placement: %w", err)
	}

	if !found {
		return false, nil
	}

	if d.Scope == database.BlobDeletionScopeAll {
		return true, nil
	}

	return backend == d.Scope, nil
}

func (w *BlobDeletionSweeper) removeBytes(ctx context.Context, d database.BlobDeletion) error {
	var err error

	if d.Scope == database.BlobDeletionScopeAll {
		err = w.store.Delete(ctx, d.SHA256)
	} else {
		err = w.store.DeleteCopy(ctx, d.Scope, d.SHA256)
	}

	if err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
		return fmt.Errorf("delete blob bytes: %w", err)
	}

	return nil
}
