// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// stalledExporter blocks every export until release is closed, like a slow
// or unreachable collector.
type stalledExporter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu       sync.Mutex
	exported int
}

func (e *stalledExporter) Export(ctx context.Context, records []sdklog.Record) error {
	e.once.Do(func() { close(e.entered) })

	select {
	case <-e.release:
	case <-ctx.Done():
		return ctx.Err()
	}

	e.mu.Lock()
	e.exported += len(records)
	e.mu.Unlock()

	return nil
}

func (*stalledExporter) Shutdown(context.Context) error   { return nil }
func (*stalledExporter) ForceFlush(context.Context) error { return nil }

func TestCountingProcessorCountsDropsByScope(t *testing.T) {
	const queue = 4

	t.Setenv("OTEL_BLRP_MAX_QUEUE_SIZE", "4")
	// Only a full batch triggers an export; the timer never fires in the test.
	t.Setenv("OTEL_BLRP_SCHEDULE_DELAY", "600000")

	dropped := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dropped_total", Help: "h"}, []string{"scope"})
	exp := &stalledExporter{entered: make(chan struct{}), release: make(chan struct{})}
	p := newCountingProcessor(exp, dropped)
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(p))

	emit := func(scope string, n int) {
		for range n {
			var r otellog.Record
			lp.Logger(scope).Emit(t.Context(), r)
		}
	}

	// A full batch goes to the exporter, which stalls holding it.
	emit("app", queue)

	select {
	case <-exp.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the batch processor never called the exporter")
	}

	// The queue fills up again; everything past it is dropped and counted
	// under the scope of the dropped record, without blocking the caller.
	emit("app", queue)
	emit("openartifact.audit", 3)

	if got := testutil.ToFloat64(dropped.WithLabelValues("openartifact.audit")); got != 3 {
		t.Errorf("dropped{scope=openartifact.audit} = %v, want 3", got)
	}

	if got := testutil.ToFloat64(dropped.WithLabelValues("app")); got != 0 {
		t.Errorf("dropped{scope=app} = %v, want 0 (records before the queue was full)", got)
	}

	close(exp.release)

	if err := lp.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Every accepted record was exported: the inner SDK queue never
	// overflowed and silently discarded one.
	if exp.exported != 2*queue {
		t.Errorf("exported %d records, want %d", exp.exported, 2*queue)
	}

	if n := p.inFlight.Load(); n != 0 {
		t.Errorf("in-flight records after shutdown = %d, want 0", n)
	}
}
