// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/telemetry"
)

// applyTelemetryEnv overrides the telemetry.otlp settings from the
// environment. Besides OPENARTIFACT_OTLP_ENABLED it reads the standard
// OpenTelemetry variables, so an existing collector setup works unchanged:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL,
//	OTEL_EXPORTER_OTLP_HEADERS (percent-decoded values),
//	OTEL_EXPORTER_OTLP_INSECURE, OTEL_SERVICE_NAME,
//	OTEL_TRACES_SAMPLER_ARG (the ratio), and the per-signal switches
//	OTEL_TRACES_EXPORTER, OTEL_METRICS_EXPORTER, OTEL_LOGS_EXPORTER (otlp|none)
//
// OPENARTIFACT_OTLP_AUDIT switches the audit signal. OTEL_SDK_DISABLED=true
// wins over everything and turns telemetry off.
func applyTelemetryEnv(o *telemetry.OTLPConfig) {
	overrideBool(&o.Enabled, "OPENARTIFACT_OTLP_ENABLED")
	overrideExporter(&o.Traces, "OTEL_TRACES_EXPORTER")
	overrideExporter(&o.Metrics, "OTEL_METRICS_EXPORTER")
	overrideExporter(&o.Logs, "OTEL_LOGS_EXPORTER")
	overrideBool(&o.Audit, "OPENARTIFACT_OTLP_AUDIT")
	overrideString(&o.Endpoint, "OTEL_EXPORTER_OTLP_ENDPOINT")
	overrideString(&o.Protocol, "OTEL_EXPORTER_OTLP_PROTOCOL")
	overrideBool(&o.Insecure, "OTEL_EXPORTER_OTLP_INSECURE")
	overrideString(&o.ServiceName, "OTEL_SERVICE_NAME")
	overrideFloat(&o.SampleRatio, "OTEL_TRACES_SAMPLER_ARG")

	// The standard format percent-encodes header values.
	env := map[string]string{}
	overrideKeyValues(&env, "OTEL_EXPORTER_OTLP_HEADERS")

	for k, v := range env {
		if o.Headers == nil {
			o.Headers = map[string]string{}
		}

		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}

		o.Headers[k] = v
	}

	var disabled bool

	overrideBool(&disabled, "OTEL_SDK_DISABLED")

	if disabled {
		o.Enabled = false
	}
}

// overrideExporter reads a standard OTEL_*_EXPORTER variable: otlp switches the
// signal on, none switches it off; any other value leaves the setting
// unchanged.
func overrideExporter(cfg *bool, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}

	switch strings.ToLower(strings.TrimSpace(v)) {
	case "otlp":
		*cfg = true
	case "none":
		*cfg = false
	}
}

func overrideFloat(cfg *float64, key string) {
	if v, ok := os.LookupEnv(key); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			warnBadEnv(key, v, err)

			return
		}

		*cfg = f
	}
}
