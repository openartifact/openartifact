// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/metrics"
)

// A burst of notifications inside the debounce window is one regeneration.
func TestRegenQueueBurstInDebounceWindowRunsOnce(t *testing.T) {
	var runs atomic.Int64

	q := newRegenQueue("test", testLogger(t), func(_ context.Context, _ string) error {
		runs.Add(1)
		return nil
	})
	q.debounce = 150 * time.Millisecond
	q.maxDelay = 10 * time.Second

	t.Cleanup(q.Stop)

	for range 50 {
		q.Notify("k")
	}

	pollUntil(t, "the regeneration", func() bool { return runs.Load() > 0 && q.idle() })

	if got := runs.Load(); got != 1 {
		t.Fatalf("burst ran %d times, want 1", got)
	}
}

// notifyIfIdle never re-arms a key that is already waiting or running, but
// starts one for a key that is idle again.
func TestRegenQueueNotifyIfIdle(t *testing.T) {
	var runs atomic.Int64

	started, gate := make(chan struct{}), make(chan struct{})

	q := newTestQueue(t, func(_ context.Context, _ string) error {
		if runs.Add(1) == 1 {
			close(started)
			<-gate
		}

		return nil
	})

	q.notifyIfIdle("k")
	<-started

	// The key is running: a read-triggered notification must not queue a rerun.
	for range 20 {
		q.notifyIfIdle("k")
	}

	close(gate)
	pollUntil(t, "queue to go idle", q.idle)

	if got := runs.Load(); got != 1 {
		t.Fatalf("notifyIfIdle on a busy key caused %d runs, want 1", got)
	}

	q.notifyIfIdle("k")
	pollUntil(t, "second run", func() bool { return runs.Load() == 2 && q.idle() })
}

// A failing regeneration is logged and settled: it is not retried (only a busy
// lock is), and a later notification regenerates again.
func TestRegenQueueFailedRunIsNotRetried(t *testing.T) {
	var runs atomic.Int64

	q := newTestQueue(t, func(_ context.Context, _ string) error {
		if runs.Add(1) == 1 {
			return errTestBoom
		}

		return nil
	})

	q.Notify("k")
	pollUntil(t, "queue to go idle", q.idle)

	if got := runs.Load(); got != 1 {
		t.Fatalf("failed run was retried: %d runs", got)
	}

	q.Notify("k")
	pollUntil(t, "second run", func() bool { return runs.Load() == 2 && q.idle() })
}

// Only finished regenerations are counted; a deferred (lock busy) attempt is
// not a regeneration.
func TestRegenQueueMetricsCountFinishedRunsOnly(t *testing.T) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	var calls atomic.Int64

	q := newTestQueue(t, func(_ context.Context, _ string) error {
		if calls.Add(1) <= 2 {
			return errLockBusy
		}

		return nil
	})
	q.setMetrics(reg)

	q.Notify("k")
	pollUntil(t, "queue to go idle", q.idle)

	if got := calls.Load(); got != 3 {
		t.Fatalf("run invoked %d times, want 3", got)
	}

	if got := testutil.ToFloat64(reg.MetadataRegenerationsTotal.WithLabelValues("test")); got != 1 {
		t.Errorf("regenerations counted = %v, want 1", got)
	}
}

// Stop does not abandon a key that is waiting out a lock-busy backoff: it is
// retried within the drain timeout and completes.
func TestRegenQueueStopWaitsForBusyRetry(t *testing.T) {
	var calls atomic.Int64

	q := newRegenQueue("test", testLogger(t), func(_ context.Context, _ string) error {
		if calls.Add(1) <= 2 {
			return errLockBusy
		}

		return nil
	})
	q.debounce = time.Millisecond
	q.retryBackoff = 20 * time.Millisecond
	q.drainTimeout = 5 * time.Second

	q.Notify("k")
	pollUntil(t, "first busy attempt", func() bool { return calls.Load() >= 1 })

	q.Stop()

	if got := calls.Load(); got != 3 {
		t.Fatalf("run invoked %d times before Stop returned, want 3 (busy, busy, done)", got)
	}
}

// A limiter below one slot selects the default capacity.
func TestNewRegenLimiterDefaultsInvalidSize(t *testing.T) {
	for _, n := range []int{0, -3} {
		if got := cap(NewRegenLimiter(n).slots); got != defaultRegenConcurrency {
			t.Errorf("NewRegenLimiter(%d) capacity = %d, want %d", n, got, defaultRegenConcurrency)
		}
	}

	if got := cap(NewRegenLimiter(7).slots); got != 7 {
		t.Errorf("NewRegenLimiter(7) capacity = %d", got)
	}
}
