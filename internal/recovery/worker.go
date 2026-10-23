// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// journalLockKey names the cluster-wide advisory lock (hashed with
	// hashtext) held by the replica draining the queue.
	journalLockKey = "openartifact:recovery-journal"

	// reconcileBudget is how many keys one reconcile step may examine.
	reconcileBudget = 2000

	// passTimeout bounds one worker pass; the next one continues.
	passTimeout = 30 * time.Minute
)

// Worker runs Journal passes every interval: drain the dirty queue, then
// advance the reconcile, one replica at a time (advisory lock on a pinned
// connection).
type Worker struct {
	j        *Journal
	db       *sql.DB
	log      *logger.Logger
	interval time.Duration

	// lock takes the cluster-wide lock; tests replace it to simulate another replica.
	lock func(ctx context.Context) (release func(), acquired bool, err error)

	trigger   chan struct{}
	running   atomic.Bool
	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	runCtx    context.Context //nolint:containedctx // lifecycle context of the worker
	cancel    context.CancelFunc
}

// NewWorker creates the worker. Start begins the schedule.
func NewWorker(j *Journal, log *logger.Logger, interval time.Duration) *Worker {
	runCtx, cancel := context.WithCancel(context.Background())

	w := &Worker{
		j: j, db: j.db, log: log, interval: interval,
		trigger: make(chan struct{}, 1),
		stopCh:  make(chan struct{}), doneCh: make(chan struct{}),
		runCtx: runCtx, cancel: cancel,
	}
	w.lock = func(ctx context.Context) (func(), bool, error) { return tryAdvisoryLock(ctx, w.db, journalLockKey) }

	return w
}

// Start begins the schedule; the first pass runs at once. Only the first call counts.
func (w *Worker) Start() {
	w.startOnce.Do(func() { go w.loop() })
}

// Stop cancels an in-flight pass and waits for the loop to exit. Idempotent and
// safe without Start.
func (w *Worker) Stop() {
	if w == nil {
		return
	}

	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.cancel()
	})

	w.startOnce.Do(func() { close(w.doneCh) })

	<-w.doneCh
}

// Trigger queues an immediate pass; false when one is already queued.
func (w *Worker) Trigger() bool {
	select {
	case w.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// Running reports whether a pass is in flight on this replica.
func (w *Worker) Running() bool { return w.running.Load() }

func (w *Worker) loop() {
	defer close(w.doneCh)

	w.runPass()

	tick := time.NewTicker(max(w.interval, time.Second))
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			w.runPass()
		case <-w.trigger:
			w.runPass()
		case <-w.stopCh:
			return
		}
	}
}

// RunPass runs one pass synchronously (tests, and the loop). It reports whether
// this replica held the lock.
func (w *Worker) RunPass(ctx context.Context) bool {
	release, ok, err := w.lock(ctx)
	if err != nil {
		w.log.Warnw("recovery journal: lock", "error", err)

		return false
	}

	if !ok {
		return false
	}

	defer release()

	w.running.Store(true)
	defer w.running.Store(false)

	ctx, cancel := context.WithTimeout(ctx, passTimeout)
	defer cancel()

	if _, failed, err := w.j.Drain(ctx); err != nil {
		if ctx.Err() == nil {
			w.log.Errorw("recovery journal: drain", "error", err)
		}

		return true
	} else if failed == 0 {
		w.j.markDrained(ctx)
	}

	// Reconcile until a table has nothing due, the queue is long, or time is up.
	for ctx.Err() == nil {
		res, err := w.j.ReconcileStep(ctx, reconcileBudget)
		if err != nil {
			w.log.Errorw("recovery journal: reconcile", "error", err)

			break
		}

		if res.Idle || res.Paused {
			break
		}

		if res.Marked > 0 {
			if _, _, err := w.j.Drain(ctx); err != nil {
				break
			}
		}
	}

	if _, err := w.j.Status(ctx); err != nil && ctx.Err() == nil {
		w.log.Debugw("recovery journal: status", "error", err)
	}

	return true
}

func (w *Worker) runPass() {
	if w.runCtx.Err() != nil {
		return
	}

	w.RunPass(w.runCtx)
}

// tryAdvisoryLock takes a session advisory lock on a pinned connection. It
// returns acquired=false (and a nil error) when another session holds it; the
// release func is always non-nil.
func tryAdvisoryLock(ctx context.Context, db *sql.DB, key string) (func(), bool, error) {
	noop := func() {}

	conn, err := db.Conn(ctx)
	if err != nil {
		return noop, false, fmt.Errorf("pin connection for advisory lock: %w", err)
	}

	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&locked); err != nil {
		_ = conn.Close()

		return noop, false, fmt.Errorf("advisory lock query: %w", err)
	}

	if !locked {
		_ = conn.Close()

		return noop, false, nil
	}

	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if _, err := conn.ExecContext(uctx, "SELECT pg_advisory_unlock(hashtext($1))", key); err != nil {
			// Unlock failed: drop the physical connection so the session lock dies with it.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}

		_ = conn.Close()
	}, true, nil
}
