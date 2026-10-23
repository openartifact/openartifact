// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// chain mounts the telemetry, tracing and logging middleware in the order
// cmd/server uses and serves one request.
func chain(t *testing.T) (*observer.ObservedLogs, *httptest.ResponseRecorder) {
	t.Helper()

	core, logs := observer.New(zapcore.InfoLevel)
	log := &logger.Logger{SugaredLogger: zap.New(core).Sugar()}

	var h http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	h = LoggingMiddleware(log)(h)
	h = TracingMiddleware(h)
	h = telemetry.HTTPMiddleware()(h)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x?token=sekret", nil)
	req.Header.Set("Traceparent", testTraceparent)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	return logs, rr
}

func TestRequestLogCarriesTraceAndSpanID(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(rec))
	uninstall := telemetry.Install(tp)

	defer uninstall()

	logs, rr := chain(t)

	entries := logs.FilterMessage("HTTP request").All()
	if len(entries) != 1 {
		t.Fatalf("%d request log lines, want 1", len(entries))
	}

	fields := entries[0].ContextMap()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans, want 1", len(spans))
	}

	sc := spans[0].SpanContext()
	if fields["trace_id"] != sc.TraceID().String() || fields["span_id"] != sc.SpanID().String() {
		t.Errorf("log ids %v/%v do not match the span %v/%v", fields["trace_id"], fields["span_id"], sc.TraceID(), sc.SpanID())
	}

	if fields["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("log trace_id %v is not the caller's", fields["trace_id"])
	}

	if got := rr.Header().Get("X-Trace-Id"); got != sc.TraceID().String() {
		t.Errorf("X-Trace-Id %q, want the OpenTelemetry trace id %q", got, sc.TraceID())
	}

	if fields["path"] != "/x" {
		t.Errorf("logged path %v keeps the query string", fields["path"])
	}
}

func TestRequestLogHasNoTraceFieldsWhenDisabled(t *testing.T) {
	logs, rr := chain(t)

	fields := logs.FilterMessage("HTTP request").All()[0].ContextMap()
	if _, ok := fields["trace_id"]; ok {
		t.Errorf("trace_id logged while telemetry is disabled: %v", fields)
	}

	if _, ok := fields["span_id"]; ok {
		t.Errorf("span_id logged while telemetry is disabled: %v", fields)
	}

	if rr.Header().Get("X-Trace-Id") == "" {
		t.Error("legacy X-Trace-Id header missing")
	}
}
