// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

const queueTestDeadline = 10 * time.Second

var errTestBoom = errors.New("boom")

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	return log
}

// newTestQueue returns a fast queue (1ms debounce/backoff) that is stopped at
// test end.
func newTestQueue[K comparable](t *testing.T, run func(ctx context.Context, key K) error) *regenQueue[K] {
	t.Helper()

	q := newRegenQueue("test", testLogger(t), run)
	q.debounce = time.Millisecond
	q.retryBackoff = time.Millisecond
	q.drainTimeout = 2 * time.Second

	t.Cleanup(q.Stop)

	return q
}

func pollUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(queueTestDeadline)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

func (q *regenQueue[K]) idle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	return len(q.entries) == 0 && q.workers == 0
}

func (q *regenQueue[K]) snapshot() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.workers, len(q.ready)
}

// inflight tracks concurrent runs and the observed maximum.
type inflight struct {
	cur, peak atomic.Int64
}

func (f *inflight) enter() {
	n := f.cur.Add(1)

	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

func (f *inflight) leave() { f.cur.Add(-1) }

func TestRegenQueueCoalescesNotifiesWhileRunning(t *testing.T) {
	const notifies = 200

	var (
		counter  atomic.Int64 // bumped by every notify
		observed atomic.Int64 // counter value read by the latest run
		runs     atomic.Int64
	)

	started := make(chan struct{})
	gate := make(chan struct{})

	q := newTestQueue(t, func(_ context.Context, _ string) error {
		observed.Store(counter.Load())

		if runs.Add(1) == 1 {
			close(started)
			<-gate
		}

		return nil
	})

	counter.Add(1)
	q.Notify("pkg")
	<-started

	var wg sync.WaitGroup

	for range notifies {
		wg.Go(func() {
			counter.Add(1)
			q.Notify("pkg")
		})
	}

	wg.Wait()
	close(gate)
	pollUntil(t, "queue to go idle", q.idle)

	if got := runs.Load(); got > 2 {
		t.Errorf("run invoked %d times for %d notifies, want <= 2", got, notifies+1)
	}

	if got, want := observed.Load(), int64(notifies+1); got != want {
		t.Errorf("final run observed counter %d, want %d (a notification was lost)", got, want)
	}
}

func TestRegenQueueDistinctKeysRespectPoolCap(t *testing.T) {
	const (
		poolCap = 3
		keys    = 40
	)

	var (
		fl   inflight
		done atomic.Int64
	)

	gate := make(chan struct{})
	q := newTestQueue(t, func(_ context.Context, _ int) error {
		fl.enter()
		defer fl.leave()

		<-gate
		done.Add(1)

		return nil
	})
	q.setLimiter(NewRegenLimiter(poolCap))

	for k := range keys {
		q.Notify(k)
	}

	pollUntil(t, "pool saturated with all other keys queued", func() bool {
		w, r := q.snapshot()
		return w == poolCap && r == keys-poolCap && fl.cur.Load() == poolCap
	})

	close(gate)
	pollUntil(t, "queue to go idle", q.idle)

	if got := fl.peak.Load(); got != poolCap {
		t.Errorf("peak concurrency = %d, want %d", got, poolCap)
	}

	if got := done.Load(); got != keys {
		t.Errorf("completed %d keys, want %d", got, keys)
	}
}

func TestRegenQueuesShareLimiter(t *testing.T) {
	const (
		poolCap = 2
		perQ    = 6
	)

	var (
		fl   inflight
		done atomic.Int64
	)

	gate := make(chan struct{})
	run := func(_ context.Context, _ int) error {
		fl.enter()
		defer fl.leave()

		<-gate
		done.Add(1)

		return nil
	}

	limiter := NewRegenLimiter(poolCap)
	qa, qb := newTestQueue(t, run), newTestQueue(t, run)
	qa.setLimiter(limiter)
	qb.setLimiter(limiter)

	for k := range perQ {
		qa.Notify(k)
		qb.Notify(k)
	}

	pollUntil(t, "shared pool saturated", func() bool { return fl.cur.Load() == poolCap })

	close(gate)
	pollUntil(t, "both queues idle", func() bool { return qa.idle() && qb.idle() })

	if got := fl.peak.Load(); got > poolCap {
		t.Errorf("peak concurrency across queues = %d, want <= %d", got, poolCap)
	}

	if got := done.Load(); got != 2*perQ {
		t.Errorf("completed %d runs, want %d", got, 2*perQ)
	}
}

func TestRegenQueueLockBusyRetries(t *testing.T) {
	tests := []struct {
		name       string
		busyFirst  int64 // number of leading runs reporting a busy lock
		maxRetries int
		wantCalls  int64
	}{
		{name: "succeeds after busy runs", busyFirst: 3, maxRetries: 8, wantCalls: 4},
		{name: "gives up after the retry budget", busyFirst: 1 << 30, maxRetries: 2, wantCalls: 3},
		{name: "no retry needed", busyFirst: 0, maxRetries: 8, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64

			q := newTestQueue(t, func(_ context.Context, _ string) error {
				if calls.Add(1) <= tt.busyFirst {
					return errLockBusy
				}

				return nil
			})
			q.maxRetries = tt.maxRetries

			q.Notify("k")
			pollUntil(t, "queue to go idle", q.idle)

			if got := calls.Load(); got != tt.wantCalls {
				t.Errorf("run invoked %d times, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestRegenQueueBusyRetryDoesNotLoseDirtyNotification(t *testing.T) {
	var (
		counter  atomic.Int64
		observed atomic.Int64
		calls    atomic.Int64
	)

	started := make(chan struct{})
	gate := make(chan struct{})

	q := newTestQueue(t, func(_ context.Context, _ string) error {
		n := calls.Add(1)
		observed.Store(counter.Load())

		if n == 1 {
			close(started)
			<-gate

			return errLockBusy
		}

		return nil
	})

	counter.Add(1)
	q.Notify("k")
	<-started

	counter.Add(1)
	q.Notify("k") // arrives while the first (soon busy) run is in flight

	close(gate)
	pollUntil(t, "queue to go idle", q.idle)

	if got, want := observed.Load(), int64(2); got != want {
		t.Errorf("last run observed %d, want %d", got, want)
	}
}

func TestRegenQueueMaxDelayBoundsDebounce(t *testing.T) {
	var ran atomic.Bool

	q := newTestQueue(t, func(_ context.Context, _ string) error {
		ran.Store(true)
		return nil
	})
	q.debounce = 200 * time.Millisecond
	q.maxDelay = 300 * time.Millisecond

	deadline := time.Now().Add(queueTestDeadline)

	// Notifying faster than the debounce would starve the run forever
	// without the max-delay cap.
	for !ran.Load() {
		if time.Now().After(deadline) {
			t.Fatal("regeneration starved by continuous notifications")
		}

		q.Notify("k")
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRegenQueueRecoversFromPanic(t *testing.T) {
	var calls atomic.Int64

	q := newTestQueue(t, func(_ context.Context, _ string) error {
		if calls.Add(1) == 1 {
			panic(errTestBoom)
		}

		return nil
	})

	q.Notify("k")
	pollUntil(t, "panicking run to finish", func() bool { return calls.Load() == 1 && q.idle() })

	q.Notify("k")
	pollUntil(t, "post-panic run", func() bool { return calls.Load() == 2 && q.idle() })
}

func TestRegenQueueStopDrainsPendingWork(t *testing.T) {
	const keys = 5

	baseline := runtime.NumGoroutine()

	var ran sync.Map

	q := newRegenQueue("test", testLogger(t), func(_ context.Context, key int) error {
		ran.Store(key, true)
		return nil
	})
	q.debounce = time.Hour

	for k := range keys {
		q.Notify(k)
	}

	q.Stop()

	for k := range keys {
		if _, ok := ran.Load(k); !ok {
			t.Errorf("key %d pending at Stop() was discarded", k)
		}
	}

	q.Notify(keys) // rejected after Stop

	if w, r := q.snapshot(); w != 0 || r != 0 || !q.idle() {
		t.Errorf("queue not idle after Stop: workers=%d ready=%d", w, r)
	}

	if _, ok := ran.Load(keys); ok {
		t.Error("notification after Stop() was executed")
	}

	pollUntil(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline })
}

func TestRegenQueueStopCancelsStuckRun(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})

	q := newRegenQueue("test", testLogger(t), func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		close(canceled)

		return ctx.Err()
	})
	q.debounce = time.Millisecond
	q.drainTimeout = 50 * time.Millisecond

	q.Notify("k")
	<-started

	q.Stop() // drain times out, then the base context is canceled

	select {
	case <-canceled:
	default:
		t.Error("run was not canceled by Stop()")
	}
}

func TestRegenQueueStopWaitsForRunningWork(t *testing.T) {
	var finished atomic.Bool

	started := make(chan struct{})
	gate := make(chan struct{})

	q := newRegenQueue("test", testLogger(t), func(_ context.Context, _ string) error {
		close(started)
		<-gate
		finished.Store(true)

		return nil
	})
	q.debounce = time.Millisecond

	q.Notify("k")
	<-started

	stopped := make(chan struct{})

	go func() {
		q.Stop()
		close(stopped)
	}()

	pollUntil(t, "Stop to reject new work", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()

		return q.closing
	})

	close(gate)
	<-stopped

	if !finished.Load() {
		t.Error("Stop() returned before the running regeneration completed")
	}
}
