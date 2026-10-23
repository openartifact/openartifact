// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Defaults for the metadata regeneration queue. Each regeneration pins at most
// two database connections (advisory lock + transaction), so the default pool
// of four runners keeps the total well below the default 25-connection pool.
const (
	defaultRegenConcurrency  = 4
	defaultRegenDebounce     = 500 * time.Millisecond
	defaultRegenMaxDelay     = 5 * time.Second
	defaultRegenRetryBackoff = 250 * time.Millisecond
	maxRegenRetryBackoff     = 5 * time.Second
	defaultRegenMaxRetries   = 8
	defaultRegenDrainTimeout = 10 * time.Second
)

// errLockBusy is returned by a regeneration whose advisory lock is held by
// another session (typically another replica). The queue reschedules the key
// with a bounded backoff instead of dropping the notification.
var errLockBusy = errors.New("could not acquire advisory lock")

var errRegenPanic = errors.New("metadata regeneration panicked")

// RegenLimiter caps how many regenerations run at once. Share one limiter
// between workers (WithRegenLimiter) so every format together respects a
// single cap; a worker without one gets a private limiter.
type RegenLimiter struct {
	slots chan struct{}
}

// NewRegenLimiter returns a limiter allowing n concurrent regenerations;
// n < 1 selects the default (4).
func NewRegenLimiter(n int) *RegenLimiter {
	if n < 1 {
		n = defaultRegenConcurrency
	}

	return &RegenLimiter{slots: make(chan struct{}, n)}
}

func (l *RegenLimiter) acquire(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}

	select {
	case l.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *RegenLimiter) release() { <-l.slots }

type regenState int

const (
	// regenWaiting: a debounce or retry-backoff timer is armed.
	regenWaiting regenState = iota
	// regenQueued: in the ready FIFO, no runner has picked it up yet.
	regenQueued
	// regenRunning: a runner is executing the regeneration.
	regenRunning
)

type regenEntry struct {
	state    regenState
	dirty    bool      // notified while running: rerun exactly once afterwards
	first    time.Time // first notification of the current debounce window
	gen      uint64    // invalidates superseded timers
	timer    *time.Timer
	attempts int // consecutive lock-busy results
}

// regenQueue is a bounded, coalescing, per-key regeneration queue. Per key
// there is at most one running and one pending (waiting or queued)
// regeneration; notifications that arrive while the key is running only set a
// dirty flag, which causes exactly one rerun. Runners are started lazily up to
// the limiter's capacity and exit when the queue is empty.
//
// Configure (set*) before the first Notify; the setters are not synchronized.
type regenQueue[K comparable] struct {
	format string
	run    func(ctx context.Context, key K) error
	log    *logger.Logger
	reg    *metrics.Registry

	debounce     time.Duration
	maxDelay     time.Duration
	retryBackoff time.Duration
	maxRetries   int
	drainTimeout time.Duration
	limiter      *RegenLimiter

	baseCtx context.Context //nolint:containedctx // queue-lifetime context, canceled by Stop
	cancel  context.CancelFunc

	mu      sync.Mutex
	entries map[K]*regenEntry
	ready   []K
	workers int
	closing bool // Stop called: new notifications are rejected, pending work drains
	stopped bool // drain finished: no more runners may start
	drained chan struct{}
	wg      sync.WaitGroup

	stopOnce sync.Once
}

func newRegenQueue[K comparable](format string, log *logger.Logger, run func(ctx context.Context, key K) error) *regenQueue[K] {
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored in q.cancel and called by Stop

	return &regenQueue[K]{
		format:       format,
		run:          run,
		log:          log,
		debounce:     defaultRegenDebounce,
		maxDelay:     defaultRegenMaxDelay,
		retryBackoff: defaultRegenRetryBackoff,
		maxRetries:   defaultRegenMaxRetries,
		drainTimeout: defaultRegenDrainTimeout,
		limiter:      NewRegenLimiter(defaultRegenConcurrency),
		baseCtx:      ctx,
		cancel:       cancel,
		entries:      make(map[K]*regenEntry),
		drained:      make(chan struct{}),
	}
}

func (q *regenQueue[K]) setDebounce(d time.Duration) { q.debounce = d }

func (q *regenQueue[K]) setLimiter(l *RegenLimiter) {
	if l != nil {
		q.limiter = l
	}
}

func (q *regenQueue[K]) setMetrics(reg *metrics.Registry) { q.reg = reg }

// Notify marks key stale. The data is read when the run executes, so a
// notification that arrives before the run starts is already covered by it.
func (q *regenQueue[K]) Notify(key K) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closing {
		q.log.Warnw("metadata regeneration dropped: worker is shutting down", "format", q.format, "key", fmt.Sprint(key))
		return
	}

	e, ok := q.entries[key]
	if !ok {
		e = &regenEntry{first: time.Now()}
		q.entries[key] = e
		q.armLocked(key, e, q.debounce)

		return
	}

	switch e.state {
	case regenRunning:
		e.dirty = true
	case regenWaiting:
		// Debounce window: push the run back, bounded by maxDelay so a
		// continuous stream cannot starve regeneration. A retry backoff
		// (attempts > 0) already covers this notification.
		if e.attempts == 0 {
			q.armLocked(key, e, q.debounceLeftLocked(e))
		}
	case regenQueued:
		// Not started yet: it will read the freshest state.
	}
}

// notifyIfIdle is Notify for a trigger that fires on every read (a cache that
// may have expired): it does nothing while key already has a regeneration
// waiting, queued or running, so steady traffic cannot keep re-arming a run
// that is already about to refresh the data.
func (q *regenQueue[K]) notifyIfIdle(key K) {
	q.mu.Lock()
	_, busy := q.entries[key]
	q.mu.Unlock()

	if !busy {
		q.Notify(key)
	}
}

func (q *regenQueue[K]) debounceLeftLocked(e *regenEntry) time.Duration {
	d := q.debounce
	if rem := max(q.maxDelay, q.debounce) - time.Since(e.first); rem < d {
		d = max(rem, 0)
	}

	return d
}

func (q *regenQueue[K]) armLocked(key K, e *regenEntry, d time.Duration) {
	if e.timer != nil {
		e.timer.Stop()
	}

	e.state = regenWaiting
	e.gen++
	gen := e.gen
	e.timer = time.AfterFunc(d, func() { q.fire(key, e, gen) })
}

func (q *regenQueue[K]) fire(key K, e *regenEntry, gen uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.entries[key] != e || e.gen != gen || e.state != regenWaiting {
		return
	}

	q.enqueueLocked(key, e)
}

func (q *regenQueue[K]) enqueueLocked(key K, e *regenEntry) {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}

	e.state = regenQueued
	q.ready = append(q.ready, key)
	q.startWorkerLocked()
}

// startWorkerLocked starts a runner when there is queued work no existing
// runner will pick up, up to the limiter capacity.
func (q *regenQueue[K]) startWorkerLocked() {
	if q.stopped || q.workers >= cap(q.limiter.slots) || q.workers >= len(q.ready) {
		return
	}

	q.workers++
	q.wg.Add(1)

	go q.worker()
}

func (q *regenQueue[K]) worker() {
	defer q.wg.Done()

	for {
		if !q.limiter.acquire(q.baseCtx) {
			q.mu.Lock()
			q.workers--
			q.mu.Unlock()

			return
		}

		key, ok := q.next()
		if !ok {
			q.limiter.release()
			return
		}

		q.execute(key)
		q.limiter.release()
	}
}

// next pops the oldest ready key and marks it running. When nothing is ready
// it deregisters the runner in the same critical section, so a concurrent
// enqueue either sees this runner (and it pops the key) or starts a new one.
func (q *regenQueue[K]) next() (K, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var zero K

	if len(q.ready) == 0 {
		q.workers--
		return zero, false
	}

	key := q.ready[0]
	q.ready[0] = zero
	q.ready = q.ready[1:]

	if len(q.ready) == 0 {
		q.ready = nil
	}

	e := q.entries[key]
	e.state = regenRunning
	e.dirty = false

	return key, true
}

func (q *regenQueue[K]) execute(key K) {
	start := time.Now()
	err := q.runSafe(key)
	busy := errors.Is(err, errLockBusy)

	if !busy && q.reg != nil {
		q.reg.MetadataRegenerationDurationSeconds.WithLabelValues(q.format).Observe(time.Since(start).Seconds())
		q.reg.MetadataRegenerationsTotal.WithLabelValues(q.format).Inc()
	}

	if err != nil && !busy {
		q.log.Errorw("metadata regeneration failed", "format", q.format, "key", fmt.Sprint(key), "error", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	e := q.entries[key]

	if busy && q.retryLocked(key, e) {
		return
	}

	e.attempts = 0

	if e.dirty {
		e.dirty = false
		e.first = time.Now()
		q.enqueueLocked(key, e)

		return
	}

	delete(q.entries, key)
	q.maybeDrainedLocked()
}

// retryLocked reschedules a lock-busy key with exponential backoff. It reports
// false when the retry budget is exhausted (the caller then settles the key).
func (q *regenQueue[K]) retryLocked(key K, e *regenEntry) bool {
	e.attempts++
	if e.attempts > q.maxRetries {
		q.log.Warnw("metadata regeneration gave up: advisory lock stayed busy",
			"format", q.format, "key", fmt.Sprint(key), "attempts", e.attempts-1)

		return false
	}

	delay := min(q.retryBackoff<<(e.attempts-1), maxRegenRetryBackoff)

	q.log.Debugw("metadata regeneration deferred: advisory lock busy",
		"format", q.format, "key", fmt.Sprint(key), "attempt", e.attempts, "retry_in", delay)

	// The retry covers every notification so far; later ones only matter if
	// they arrive while it runs (dirty).
	e.dirty = false
	e.first = time.Now()
	q.armLocked(key, e, delay)

	return true
}

func (q *regenQueue[K]) runSafe(key K) (err error) {
	defer func() {
		if r := recover(); r != nil {
			q.log.Errorw("panic in metadata regeneration", "format", q.format, "key", fmt.Sprint(key), "panic", r, "stack", string(debug.Stack()))

			err = fmt.Errorf("%w: %v", errRegenPanic, r)
		}
	}()

	return q.run(q.baseCtx, key)
}

func (q *regenQueue[K]) maybeDrainedLocked() {
	if !q.closing || len(q.entries) != 0 {
		return
	}

	select {
	case <-q.drained:
	default:
		close(q.drained)
	}
}

// Stop rejects new notifications, runs every debouncing key immediately,
// waits up to the drain timeout for running and queued work (lock-busy retries
// keep their backoff), then cancels the base context and waits for the runners
// to exit. It is idempotent and safe to call concurrently.
func (q *regenQueue[K]) Stop() {
	q.stopOnce.Do(q.stop)
}

func (q *regenQueue[K]) stop() {
	q.mu.Lock()
	q.closing = true

	for key, e := range q.entries {
		if e.state == regenWaiting && e.attempts == 0 {
			q.enqueueLocked(key, e)
		}
	}

	q.maybeDrainedLocked()
	q.mu.Unlock()

	timer := time.NewTimer(q.drainTimeout)
	defer timer.Stop()

	select {
	case <-q.drained:
	case <-timer.C:
		q.mu.Lock()
		pending := len(q.entries)
		q.mu.Unlock()
		q.log.Warnw("metadata regeneration drain timed out; abandoning remaining work", "format", q.format, "pending", pending)
	}

	q.mu.Lock()
	q.stopped = true

	for _, e := range q.entries {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
	q.mu.Unlock()

	q.cancel()

	done := make(chan struct{})

	go func() {
		q.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(q.drainTimeout):
		q.log.Warnw("metadata regeneration runners did not exit after cancellation", "format", q.format)
	}
}
