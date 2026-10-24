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
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// auditRetentionLockKey names the cluster-wide advisory lock (hashed by
	// tryAdvisoryLock) that the replica running a retention pass holds.
	auditRetentionLockKey = "openartifact:audit-retention"

	// auditRetentionBatchPause is the pace of a pass: after a full batch the
	// next one waits for this ticker, so a large backlog is deleted without
	// hogging the database or flooding replication with one huge transaction
	// stream.
	auditRetentionBatchPause = 200 * time.Millisecond

	// auditRetentionRunTimeout bounds one scheduled pass (and the pinned lock
	// connection); the next pass continues where it stopped.
	auditRetentionRunTimeout = 30 * time.Minute
)

// AuditRetentionStats is the outcome of one retention pass.
type AuditRetentionStats struct {
	// Deleted counts the audit entries removed.
	Deleted int64
	// Batches counts the delete statements that ran, the last (short or empty)
	// one included.
	Batches int
	// Busy is set when another replica holds the retention lock; nothing was
	// done.
	Busy bool
}

// AuditRetentionWorker deletes audit entries older than audit.retentionDays,
// oldest first in batches of a fixed size, paced by a ticker. Replicas are
// excluded from each other by a Postgres advisory lock held on a pinned
// connection for the whole pass, so the work is done once per cycle however
// many replicas run. Interrupted passes lose nothing: every batch commits on
// its own and the next pass continues with the oldest remaining rows.
//
// Nothing is exported before the deletion; see docs/operations/audit-retention.md.
type AuditRetentionWorker struct {
	db            database.Database
	store         database.AuditRetentionStore
	log           *logger.Logger
	retentionDays int
	interval      time.Duration
	batch         int
	pause         time.Duration
	reg           *metrics.Registry

	// lock takes the cluster-wide retention lock and now reads the clock; both
	// are fields so tests can simulate another replica and move time.
	lock func(ctx context.Context) (release func(), acquired bool, err error)
	now  func() time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	// runCtx is cancelled by Stop so an in-flight pass stops promptly.
	runCtx context.Context //nolint:containedctx // lifecycle context of the worker
	cancel context.CancelFunc
}

// NewAuditRetentionWorker creates the worker. retentionDays is the age past
// which entries are deleted (0 or less keeps everything: Start and RunOnce do
// nothing), interval the pause between scheduled passes and batch the entries
// one delete statement removes. Without a database that implements
// database.AuditRetentionStore the worker does nothing.
func NewAuditRetentionWorker(db database.Database, log *logger.Logger, retentionDays int, interval time.Duration, batch int) *AuditRetentionWorker {
	runCtx, cancel := context.WithCancel(context.Background())

	store, _ := db.(database.AuditRetentionStore)

	w := &AuditRetentionWorker{
		db: db, store: store, log: log,
		retentionDays: retentionDays, interval: interval, batch: max(batch, 1),
		pause:  auditRetentionBatchPause,
		now:    time.Now,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
		runCtx: runCtx, cancel: cancel,
	}
	w.lock = w.acquireLock

	return w
}

// WithMetrics counts the deleted entries and stamps the last finished pass.
func (w *AuditRetentionWorker) WithMetrics(reg *metrics.Registry) *AuditRetentionWorker {
	w.reg = reg

	return w
}

func (w *AuditRetentionWorker) enabled() bool {
	return w.retentionDays > 0 && w.store != nil
}

// Start runs a first pass right away and then one per interval. It does
// nothing when retention is off, the interval is not positive or the database
// has no audit retention support. Only the first call counts.
func (w *AuditRetentionWorker) Start() {
	w.startOnce.Do(func() {
		if !w.enabled() || w.interval <= 0 {
			close(w.doneCh)

			return
		}

		go w.loop()
	})
}

// Stop cancels an in-flight pass and waits for the schedule goroutine to exit.
// It is idempotent and safe without Start.
func (w *AuditRetentionWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.cancel()
	})

	// Never started: nothing will close doneCh, and a later Start must not.
	w.startOnce.Do(func() { close(w.doneCh) })

	<-w.doneCh
}

func (w *AuditRetentionWorker) loop() {
	defer close(w.doneCh)

	w.runScheduled()

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
func (w *AuditRetentionWorker) runScheduled() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in audit retention pass", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.runCtx, auditRetentionRunTimeout)
	defer cancel()

	stats, err := w.RunOnce(ctx)
	if err != nil && w.runCtx.Err() == nil {
		w.log.Errorw("audit retention failed", "error", err, "deleted", stats.Deleted)
	}

	if stats.Deleted > 0 {
		w.log.Infow("audit retention finished",
			"deleted", stats.Deleted, "batches", stats.Batches, "retention_days", w.retentionDays)
	}
}

// RunOnce runs one retention pass: it deletes, batch by batch, every audit
// entry created before now minus the retention until a batch comes back short
// or ctx ends. A pass that finds the lock held by another replica does nothing
// and reports Busy. The error is non-nil only when the pass could not run to
// the end (lock or delete failure, cancelled ctx); entries deleted before that
// stay deleted and are counted in the stats.
func (w *AuditRetentionWorker) RunOnce(ctx context.Context) (AuditRetentionStats, error) {
	var stats AuditRetentionStats

	if !w.enabled() {
		return stats, nil
	}

	release, acquired, err := w.lock(ctx)
	if err != nil {
		return stats, fmt.Errorf("lock audit retention: %w", err)
	}

	if !acquired {
		stats.Busy = true

		return stats, nil
	}
	defer release()

	cutoff := w.now().Add(-time.Duration(w.retentionDays) * 24 * time.Hour)

	if err := w.purgeBefore(ctx, cutoff, &stats); err != nil {
		return stats, err
	}

	if w.reg != nil {
		w.reg.AuditRetentionLastRunSeconds.SetToCurrentTime()
	}

	return stats, nil
}

// acquireLock takes the advisory lock on a pinned connection. A database
// without an SQL pool (test fakes) has no other replicas to exclude.
func (w *AuditRetentionWorker) acquireLock(ctx context.Context) (func(), bool, error) {
	sqlDB := w.db.GetDB()
	if sqlDB == nil {
		return func() {}, true, nil
	}

	return tryAdvisoryLock(ctx, sqlDB, auditRetentionLockKey)
}

// purgeBefore deletes batches until one is short. The ticker, not a sleep,
// paces the batches: the first goes out at once, each further one waits for a
// tick or the end of ctx.
func (w *AuditRetentionWorker) purgeBefore(ctx context.Context, cutoff time.Time, stats *AuditRetentionStats) error {
	ticker := time.NewTicker(w.pause)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("audit retention interrupted: %w", err)
		}

		n, err := w.store.PurgeAuditLogsBefore(ctx, cutoff, w.batch)
		if err != nil {
			return fmt.Errorf("delete audit batch: %w", err)
		}

		stats.Batches++
		stats.Deleted += n

		if w.reg != nil {
			w.reg.AuditRetentionDeletedTotal.Add(float64(n))
		}

		if n < int64(w.batch) {
			return nil
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("audit retention interrupted: %w", ctx.Err())
		}
	}
}
