// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"

	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/tests/integration/otelcoltest"
)

// TestOTLPLogRecordReachesCollector exports a log record through the provider
// telemetry.Setup installs, over both OTLP protocols, to a real collector and
// reads it back from the collector's file exporter.
func TestOTLPLogRecordReachesCollector(t *testing.T) {
	col := otelcoltest.Start(t)

	for _, tc := range []struct{ protocol, endpoint string }{
		{telemetry.ProtocolGRPC, col.GRPCEndpoint},
		{telemetry.ProtocolHTTP, col.HTTPEndpoint},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			cfg := telemetry.DefaultConfig()
			cfg.OTLP.Enabled = true
			cfg.OTLP.Protocol = tc.protocol
			cfg.OTLP.Endpoint = tc.endpoint
			cfg.OTLP.Insecure = true
			cfg.OTLP.ServiceName = "oa-otlp-logs-it"
			cfg.OTLP.Traces = false
			cfg.OTLP.Audit = true

			dropped := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "dropped_total", Help: "h"}, []string{"scope"})

			shutdown, err := telemetry.Setup(t.Context(), cfg, telemetry.Build{Version: "it"}, nil, dropped, nil)
			if err != nil {
				t.Fatal(err)
			}

			body := "otlp-log-it-" + tc.protocol

			var rec otellog.Record
			rec.SetBody(attribute.StringValue(body))
			rec.SetSeverity(otellog.SeverityInfo)
			otel.GetLoggerProvider().Logger("openartifact.audit").Emit(t.Context(), rec)

			// Shutdown flushes the batch processor to the collector.
			if err := shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}

			line := col.WaitForLine(t, 30*time.Second, func(l string) bool { return strings.Contains(l, body) })

			for _, want := range []string{`"resourceLogs"`, `"openartifact.audit"`, `"oa-otlp-logs-it"`} {
				if !strings.Contains(line, want) {
					t.Errorf("exported line lacks %s: %s", want, line)
				}
			}

			if n := testutil.CollectAndCount(dropped); n != 0 {
				t.Errorf("dropped series = %d, want none", n)
			}
		})
	}
}

// The otlp* types are the parts of the OTLP/JSON export requests, as the
// collector's file exporter writes them, that the test reads. They have no
// json tags: encoding/json matches the camelCase OTLP keys (traceId,
// resourceLogs) to the field names case-insensitively.
type otlpKV struct {
	Key   string
	Value struct{ StringValue string }
}

type otlpResource struct{ Attributes []otlpKV }

func (r otlpResource) attr(key string) string {
	for _, kv := range r.Attributes {
		if kv.Key == key {
			return kv.Value.StringValue
		}
	}

	return ""
}

type otlpLogRecord struct {
	SeverityNumber int
	SeverityText   string
	TraceID        string
	SpanID         string
	Body           struct{ StringValue string }
}

type otlpLogs struct {
	ResourceLogs []struct {
		Resource  otlpResource
		ScopeLogs []struct {
			Scope      struct{ Name string }
			LogRecords []otlpLogRecord
		}
	}
}

type otlpSpan struct {
	TraceID string
	SpanID  string
	Kind    int
}

type otlpTraces struct {
	ResourceSpans []struct {
		ScopeSpans []struct{ Spans []otlpSpan }
	}
}

// spanKindServer is SPAN_KIND_SERVER in OTLP/JSON.
const spanKindServer = 2

// TestOTLPRequestLogsCarryTheRequestSpan serves a request through the server's
// telemetry, tracing and logging middleware with telemetry.otlp.logs on, the
// logger teed with the OTLP bridge as cmd/server does, and reads back from a
// real collector the server span and the records logged for the request: the
// access log and a handler log line. Each record carries the request span's
// trace and span ids as its own, the zap level as its severity and the
// provider's service.name.
func TestOTLPRequestLogsCarryTheRequestSpan(t *testing.T) {
	const (
		service    = "oa-otlp-request-logs-it"
		handlerMsg = "otlp-request-log-it handler"
	)

	col := otelcoltest.Start(t)

	cfg := telemetry.DefaultConfig()
	cfg.OTLP.Enabled = true
	cfg.OTLP.Endpoint = col.GRPCEndpoint
	cfg.OTLP.Insecure = true
	cfg.OTLP.ServiceName = service
	cfg.OTLP.Logs = true

	shutdown, err := telemetry.Setup(t.Context(), cfg, telemetry.Build{Version: "it"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	stdout, err := logger.New("info", "json", filepath.Join(t.TempDir(), "stdout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	log, err := stdout.Tee(telemetry.LogCore())
	if err != nil {
		t.Fatal(err)
	}

	var h http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		log.Infow(handlerMsg, telemetry.LogFields(r.Context())...)
	})
	h = middleware.LoggingMiddleware(log)(h)
	h = middleware.TracingMiddleware(h)
	h = telemetry.HTTPMiddleware()(h)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/it", nil))

	// Shutdown flushes the span and the records to the collector.
	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	span := exportedServerSpan(t, col)

	for _, msg := range []string{handlerMsg, "HTTP request"} {
		rec, svc := exportedLogRecord(t, col, msg)

		if rec.TraceID == "" || rec.TraceID != span.TraceID || rec.SpanID != span.SpanID {
			t.Errorf("%q: record ids %s/%s, want the request span %s/%s", msg, rec.TraceID, rec.SpanID, span.TraceID, span.SpanID)
		}

		if rec.SeverityNumber != int(otellog.SeverityInfo) || rec.SeverityText != "info" {
			t.Errorf("%q: severity %d %q, want %d \"info\"", msg, rec.SeverityNumber, rec.SeverityText, otellog.SeverityInfo)
		}

		if svc != service {
			t.Errorf("%q: resource service.name %q, want %q", msg, svc, service)
		}
	}
}

// exportedServerSpan waits for the server span and returns it.
func exportedServerSpan(t *testing.T, col *otelcoltest.Collector) otlpSpan {
	t.Helper()

	var span otlpSpan

	col.WaitForLine(t, 30*time.Second, func(l string) bool {
		var tr otlpTraces
		if json.Unmarshal([]byte(l), &tr) != nil {
			return false
		}

		for _, rs := range tr.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					if s.Kind == spanKindServer {
						span = s

						return true
					}
				}
			}
		}

		return false
	})

	return span
}

// exportedLogRecord waits for the application record whose body is msg and
// returns it with the service.name of its resource.
func exportedLogRecord(t *testing.T, col *otelcoltest.Collector, msg string) (otlpLogRecord, string) {
	t.Helper()

	var (
		rec     otlpLogRecord
		service string
	)

	col.WaitForLine(t, 30*time.Second, func(l string) bool {
		var lg otlpLogs
		if json.Unmarshal([]byte(l), &lg) != nil {
			return false
		}

		for _, rl := range lg.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, r := range sl.LogRecords {
					if r.Body.StringValue == msg && sl.Scope.Name == "github.com/openartifact/openartifact" {
						rec, service = r, rl.Resource.attr("service.name")

						return true
					}
				}
			}
		}

		return false
	})

	return rec, service
}
