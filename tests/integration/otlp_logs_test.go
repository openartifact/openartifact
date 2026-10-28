// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"

	"github.com/openartifact/openartifact/internal/telemetry"
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
