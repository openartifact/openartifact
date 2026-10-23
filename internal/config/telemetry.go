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
//	OTEL_TRACES_SAMPLER_ARG (the ratio), OTEL_METRICS_EXPORTER (otlp|none)
//
// OTEL_SDK_DISABLED=true wins over everything and turns telemetry off.
func applyTelemetryEnv(o *telemetry.OTLPConfig) {
	overrideBool(&o.Enabled, "OPENARTIFACT_OTLP_ENABLED")
	overrideString(&o.Endpoint, "OTEL_EXPORTER_OTLP_ENDPOINT")
	overrideString(&o.Protocol, "OTEL_EXPORTER_OTLP_PROTOCOL")
	overrideBool(&o.Insecure, "OTEL_EXPORTER_OTLP_INSECURE")
	overrideString(&o.ServiceName, "OTEL_SERVICE_NAME")
	overrideFloat(&o.SampleRatio, "OTEL_TRACES_SAMPLER_ARG")

	if v, ok := os.LookupEnv("OTEL_METRICS_EXPORTER"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "otlp":
			o.Metrics = true
		case "none":
			o.Metrics = false
		}
	}

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
