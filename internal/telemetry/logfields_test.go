// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	otellog "go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// recordingExporter keeps every exported log record.
type recordingExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *recordingExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}

	return nil
}

func (*recordingExporter) Shutdown(context.Context) error   { return nil }
func (*recordingExporter) ForceFlush(context.Context) error { return nil }

// TestLogCoreEmitsUnderTheLoggedSpan logs through the stdout encoder teed with
// LogCore, as cmd/server does with telemetry.otlp.logs on: the record carries
// the span's ids as its own trace and span ids, while the encoded line is
// unchanged (the two id fields, no context field).
func TestLogCoreEmitsUnderTheLoggedSpan(t *testing.T) {
	install(t)

	exp := &recordingExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	otel.SetLoggerProvider(lp)
	t.Cleanup(func() {
		otel.SetLoggerProvider(lognoop.NewLoggerProvider())
		_ = lp.Shutdown(context.Background())
	})

	var out bytes.Buffer

	stdout := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{MessageKey: "message"}),
		zapcore.AddSync(&out),
		zapcore.InfoLevel,
	)
	log := zap.New(zapcore.NewTee(stdout, LogCore())).Sugar()

	ctx, span := otel.Tracer(scope).Start(t.Context(), "s")
	defer span.End()

	log.Warnw("HTTP request", append([]any{"status", 503}, LogFields(ctx)...)...)

	sc := span.SpanContext()

	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("stdout line %q: %v", out.String(), err)
	}

	want := map[string]any{
		"message":  "HTTP request",
		"status":   float64(503),
		"trace_id": sc.TraceID().String(),
		"span_id":  sc.SpanID().String(),
	}
	if len(line) != len(want) {
		t.Errorf("stdout line = %v, want exactly %v", line, want)
	}

	for k, v := range want {
		if line[k] != v {
			t.Errorf("stdout %s = %v, want %v", k, line[k], v)
		}
	}

	exp.mu.Lock()
	defer exp.mu.Unlock()

	if len(exp.records) != 1 {
		t.Fatalf("%d records exported, want 1", len(exp.records))
	}

	r := exp.records[0]
	if r.TraceID() != sc.TraceID() || r.SpanID() != sc.SpanID() {
		t.Errorf("record ids %s/%s, want the span's %s/%s", r.TraceID(), r.SpanID(), sc.TraceID(), sc.SpanID())
	}

	if r.Severity() != otellog.SeverityWarn || r.InstrumentationScope().Name != scope {
		t.Errorf("record severity %v scope %q, want %v %q", r.Severity(), r.InstrumentationScope().Name, otellog.SeverityWarn, scope)
	}
}
