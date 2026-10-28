// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// defaultLogQueueSize is the SDK default of OTEL_BLRP_MAX_QUEUE_SIZE.
const defaultLogQueueSize = 2048

// countingProcessor is the SDK BatchProcessor with a counted drop. The SDK
// processor never blocks the caller: when its queue is full it overwrites the
// oldest record and only logs the number through the OTel internal logger, so
// a drop is invisible to Prometheus and carries no scope. This wrapper keeps
// the records in flight (emitted, not yet handed to the exporter) within the
// queue size, so the inner queue never overflows, and drops the newest record
// itself instead: OnEmit stays non-blocking and every drop is counted under the
// scope of the record that was dropped.
type countingProcessor struct {
	*sdklog.BatchProcessor

	inFlight atomic.Int64
	limit    int64
	dropped  *prometheus.CounterVec
}

// newCountingProcessor batches records for exp. dropped is incremented with
// the instrumentation scope name of every record turned away; nil counts
// nothing.
func newCountingProcessor(exp sdklog.Exporter, dropped *prometheus.CounterVec) *countingProcessor {
	size := logQueueSize()
	p := &countingProcessor{limit: int64(size), dropped: dropped}
	p.BatchProcessor = sdklog.NewBatchProcessor(
		&releasingExporter{Exporter: exp, inFlight: &p.inFlight},
		sdklog.WithMaxQueueSize(size),
	)

	return p
}

// OnEmit queues r unless limit records are already in flight.
func (p *countingProcessor) OnEmit(ctx context.Context, r *sdklog.Record) error {
	if p.inFlight.Add(1) > p.limit {
		p.inFlight.Add(-1)

		if p.dropped != nil {
			p.dropped.WithLabelValues(r.InstrumentationScope().Name).Inc()
		}

		return nil
	}

	return p.BatchProcessor.OnEmit(ctx, r) //nolint:wrapcheck // processor contract, returned as is
}

// releasingExporter releases the in-flight slots of the records the batch
// processor hands to the exporter, before the (possibly slow) export runs:
// from then on they are out of the queue.
//
// SIMPLIFIED: a ForceFlush whose context expires while the SDK exports the
// flushed records in chunks skips the remaining chunks, and their slots are
// never released, so the limit shrinks by that many records. Nothing calls
// ForceFlush on the log provider today (Shutdown only, after which the slots
// no longer matter); a caller that does would need the slots resynchronised
// from the processor's queue length.
type releasingExporter struct {
	sdklog.Exporter

	inFlight *atomic.Int64
}

func (e *releasingExporter) Export(ctx context.Context, records []sdklog.Record) error {
	e.inFlight.Add(-int64(len(records)))

	return e.Exporter.Export(ctx, records) //nolint:wrapcheck // exporter contract, returned as is
}

// logQueueSize resolves the batch queue size the way the SDK does
// (OTEL_BLRP_MAX_QUEUE_SIZE, else 2048) so the processor and its in-flight
// limit agree; the result is passed to the SDK explicitly.
func logQueueSize() int {
	if n, err := strconv.Atoi(os.Getenv("OTEL_BLRP_MAX_QUEUE_SIZE")); err == nil && n > 0 {
		return n
	}

	return defaultLogQueueSize
}
