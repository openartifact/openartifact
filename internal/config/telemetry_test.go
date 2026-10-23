// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTelemetryDefaultsOff(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	o := cfg.Telemetry.OTLP
	if o.Enabled || o.Metrics || o.Protocol != "grpc" || o.ServiceName != "openartifact" || o.SampleRatio != 1 || o.Endpoint != "" {
		t.Errorf("defaults = %+v", o)
	}
}

func TestTelemetryYAMLAndEnvPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")

	yml := `telemetry:
  otlp:
    enabled: true
    endpoint: yaml-collector:4317
    protocol: grpc
    headers: {x-from-yaml: "1", x-both: yaml}
    insecure: false
    serviceName: from-yaml
    sampleRatio: 0.5
    metrics: false
`
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if o := cfg.Telemetry.OTLP; !o.Enabled || o.Endpoint != "yaml-collector:4317" || o.ServiceName != "from-yaml" || o.SampleRatio != 0.5 || o.Headers["x-from-yaml"] != "1" {
		t.Fatalf("yaml not applied: %+v", o)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://env-collector:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer%20tok,x-both=env")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	t.Setenv("OTEL_SERVICE_NAME", "from-env")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	t.Setenv("OTEL_METRICS_EXPORTER", "otlp")

	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}

	o := cfg.Telemetry.OTLP
	if o.Endpoint != "http://env-collector:4318" || o.Protocol != "http/protobuf" || !o.Insecure ||
		o.ServiceName != "from-env" || o.SampleRatio != 0.25 || !o.Metrics {
		t.Errorf("env not applied: %+v", o)
	}

	if o.Headers["authorization"] != "Bearer tok" || o.Headers["x-both"] != "env" || o.Headers["x-from-yaml"] != "1" {
		t.Errorf("headers = %v", o.Headers)
	}
}

func TestTelemetryEnableAndKillSwitch(t *testing.T) {
	t.Setenv("OPENARTIFACT_OTLP_ENABLED", "true")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.Telemetry.OTLP.Enabled {
		t.Fatal("OPENARTIFACT_OTLP_ENABLED=true did not enable telemetry")
	}

	t.Setenv("OTEL_SDK_DISABLED", "true")

	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Telemetry.OTLP.Enabled {
		t.Error("OTEL_SDK_DISABLED=true did not win")
	}
}

func TestTelemetryEndpointOnlyDoesNotEnable(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector:4317")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Telemetry.OTLP.Enabled {
		t.Error("an endpoint variable alone must not turn telemetry on")
	}
}

func TestValidate_Telemetry(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Telemetry.OTLP.Enabled = true
	cfg.Telemetry.OTLP.SampleRatio = 2

	if err := cfg.Validate(); err == nil {
		t.Error("sampleRatio 2 accepted")
	}
}
