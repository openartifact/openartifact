// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// tierMoverLockKey names the cluster-wide advisory lock (hashed by
	// pglock.Try) held by the replica running a mover pass, so passes of
	// different replicas never overlap.
	tierMoverLockKey = "openartifact:tier-mover"

	// tierMoverRunTimeout bounds one pass; the next one continues where it stopped.
	tierMoverRunTimeout = 2 * time.Hour
)

// TierPassRunner runs one mover pass; *tiering.Engine implements it.
type TierPassRunner interface {
	RunPass(ctx context.Context) (tiering.PassStats, error)
}

// TierMoverWorker schedules tiering.Engine passes: every interval, and on
// demand through Trigger. A pass holds a Postgres advisory lock on a pinned
// connection, so with several replicas exactly one moves blobs at a time; the
// engine itself is crash-safe and resumable (see docs/operations/storage-tiering.md),
// so killing the process mid-pass loses nothing.
type TierMoverWorker struct {
	*lifecycle

	engine   TierPassRunner
	log      *logger.Logger
	interval time.Duration
	enabled  bool

	// lock takes the cluster-wide mover lock. It is a field so tests can
	// simulate another replica holding it.
	lock func(ctx context.Context) (release func(), acquired bool, err error)

	trigger chan struct{}
	running atomic.Bool
	last    atomic.Pointer[tiering.PassStats]
}

// NewTierMoverWorker creates the worker. sqlDB is the pool the pinned lock
// connection comes from (nil means no other replicas to exclude). When enabled
// is false the worker never moves anything and Trigger reports false.
func NewTierMoverWorker(engine TierPassRunner, sqlDB *sql.DB, log *logger.Logger, interval time.Duration, enabled bool) *TierMoverWorker {
	w := &TierMoverWorker{
		lifecycle: newLifecycle(),
		engine:    engine, log: log, interval: interval, enabled: enabled,
		trigger: make(chan struct{}, 1),
	}

	w.lock = func(ctx context.Context) (func(), bool, error) { return tryClusterLock(ctx, sqlDB, tierMoverLockKey) }

	return w
}

// Start begins the schedule. Only the first call counts.
func (w *TierMoverWorker) Start() {
	w.startOnce.Do(func() {
		if !w.enabled {
			close(w.doneCh)

			return
		}

		go w.loop()
	})
}

// Trigger queues an immediate pass. It reports false when the worker is
// disabled or a pass is already running with another one queued behind it.
func (w *TierMoverWorker) Trigger() bool {
	if !w.enabled {
		return false
	}

	select {
	case w.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// Status reports the worker's state.
func (w *TierMoverWorker) Status() tiering.RunnerStatus {
	return tiering.RunnerStatus{
		Enabled: w.enabled, Running: w.running.Load(),
		IntervalSeconds: int64(w.interval / time.Second), LastPass: w.last.Load(),
	}
}

func (w *TierMoverWorker) loop() {
	defer close(w.doneCh)

	var tick <-chan time.Time

	if w.interval > 0 {
		t := time.NewTicker(w.interval)
		defer t.Stop()

		tick = t.C
	}

	for {
		select {
		case <-tick:
			w.runScheduled()
		case <-w.trigger:
			w.runScheduled()
		case <-w.stopCh:
			return
		}
	}
}

// runScheduled runs one pass. A panic ends the pass, not the schedule.
func (w *TierMoverWorker) runScheduled() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in tier mover pass", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.runCtx, tierMoverRunTimeout)
	defer cancel()

	stats, busy, err := w.RunOnce(ctx)

	switch {
	case err != nil && w.runCtx.Err() == nil:
		w.log.Errorw("tier mover pass failed", "error", err)
	case busy:
		w.log.Debugw("tier mover pass skipped: another replica holds the lock")
	case stats.Moved+stats.Promoted+stats.Recovered+stats.Failed > 0:
		w.log.Infow("tier mover pass finished",
			"moved", stats.Moved, "promoted", stats.Promoted, "recovered", stats.Recovered,
			"skipped", stats.Skipped, "failed", stats.Failed, "bytes", stats.Bytes, "scanned", stats.Scanned)
	}
}

// RunOnce runs one pass under the cluster lock. busy is true when another
// replica holds the lock (nothing was done).
func (w *TierMoverWorker) RunOnce(ctx context.Context) (tiering.PassStats, bool, error) {
	release, acquired, err := w.lock(ctx)
	if err != nil {
		return tiering.PassStats{}, false, err
	}

	if !acquired {
		return tiering.PassStats{}, true, nil
	}

	defer release()

	w.running.Store(true)
	defer w.running.Store(false)

	stats, err := w.engine.RunPass(ctx)
	w.last.Store(&stats)

	return stats, false, err
}
