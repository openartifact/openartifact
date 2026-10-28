// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/telemetry"
)

// setupTelemetry bridges the application logger to OTLP only with
// telemetry.otlp.logs on: a line logged through a.log afterwards reaches the
// collector, and with the log provider started for audit alone it does not.
func TestSetupTelemetryBridgesLogsOnlyWhenLogsAreOn(t *testing.T) {
	const probe = "otlp-startup-gate-probe"

	for _, tc := range []struct {
		name string
		logs bool
	}{
		{"logs on", true},
		{"logs off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				exported bool
			)

			col := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)

				mu.Lock()
				exported = exported || (r.URL.Path == "/v1/logs" && bytes.Contains(body, []byte(probe)))
				mu.Unlock()

				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(col.Close)

			cfg := &config.Config{Telemetry: telemetry.DefaultConfig()}
			cfg.Telemetry.OTLP.Enabled = true
			cfg.Telemetry.OTLP.Protocol = telemetry.ProtocolHTTP
			cfg.Telemetry.OTLP.Endpoint = col.Listener.Addr().String()
			cfg.Telemetry.OTLP.Insecure = true
			cfg.Telemetry.OTLP.Traces = false
			cfg.Telemetry.OTLP.Audit = true
			cfg.Telemetry.OTLP.Logs = tc.logs

			a, _ := newTestApp(t, cfg)

			if err := a.setupTelemetry(); err != nil {
				t.Fatal(err)
			}

			a.log.Infow(probe)

			// close shuts the provider down, which flushes its batch.
			a.close()

			mu.Lock()
			defer mu.Unlock()

			if exported != tc.logs {
				t.Errorf("line reached the collector = %t, want %t", exported, tc.logs)
			}
		})
	}
}
