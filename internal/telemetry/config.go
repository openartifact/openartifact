// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telemetry wires OpenTelemetry: the OTLP exporters (traces, a bridge
// that ships the Prometheus registry as OTLP metrics, and a log provider for
// application and audit records), the HTTP server middleware, an instrumented
// client transport and a pgx tracer.
//
// Everything is off unless Config.OTLP.Enabled is set, and each signal has its
// own switch under it. Without traces no tracer provider is installed, no
// middleware is added and the transport and pgx tracer reduce to an atomic
// load, so a build without tracing behaves as it did before.
//
// Attributes never carry credentials or user data: spans are named after the
// route template (never the raw path), URLs are reduced to host and scheme
// (never the query string or userinfo), and SQL is reduced to the sqlc
// statement name (never the text or its arguments).
package telemetry

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
)

// Protocols accepted by Config.OTLP.Protocol.
const (
	ProtocolGRPC = "grpc"
	ProtocolHTTP = "http"
)

// DefaultServiceName is the service.name reported when none is configured.
const DefaultServiceName = "openartifact"

var (
	errProtocol         = errors.New("telemetry.otlp.protocol must be grpc, http or http/protobuf")
	errSampleRatio      = errors.New("telemetry.otlp.sampleRatio must be between 0 and 1")
	errEndpoint         = errors.New("telemetry.otlp.endpoint must be host:port or an http(s) URL without query or fragment")
	errHeaderName       = errors.New("telemetry.otlp.headers must not contain an empty header name")
	errFrontendEndpoint = errors.New("telemetry.frontend.endpoint must be an http(s) URL without query or fragment")
	errFrontendNoTarget = errors.New("telemetry.frontend.enabled needs telemetry.frontend.endpoint (an OTLP/HTTP URL) " +
		"unless telemetry.otlp.protocol is http with an endpoint")
)

// Config is the telemetry section of the server configuration.
type Config struct {
	OTLP     OTLPConfig     `yaml:"otlp"`
	Frontend FrontendConfig `yaml:"frontend"`
}

// FrontendConfig configures the experimental browser telemetry: the web UI
// loads the OpenTelemetry browser SDK and sends its errors, web vitals and API
// client spans to a same-origin relay, which forwards them unchanged as
// OTLP/HTTP to the collector. It needs telemetry.otlp.enabled (the shared
// headers and TLS) and has no compatibility promise.
type FrontendConfig struct {
	// Enabled turns the whole feature on. Off by default: the UI then loads no
	// telemetry code and the relay and its config endpoint answer 404.
	Enabled bool `yaml:"enabled"`
	// Endpoint is the OTLP/HTTP base URL the relay forwards to ("/v1/traces"
	// and "/v1/logs" are appended). Empty uses telemetry.otlp.endpoint, which
	// then has to be an OTLP/HTTP one (protocol http).
	Endpoint string `yaml:"endpoint"`
}

// FrontendActive reports whether the frontend telemetry relay is on: the
// feature toggle and the OTLP master switch.
func (c Config) FrontendActive() bool { return c.Frontend.Enabled && c.OTLP.Enabled }

// FrontendURL is the collector URL the relay forwards signal ("traces" or
// "logs") to. It assumes a configuration Validate accepted with the frontend
// active.
func (c Config) FrontendURL(signal string) string {
	if c.Frontend.Endpoint != "" {
		return strings.TrimRight(strings.TrimSpace(c.Frontend.Endpoint), "/") + "/v1/" + signal
	}

	ep, _ := parseEndpoint(c.OTLP.Endpoint)

	scheme := "https"
	if ep.plaintext || c.OTLP.Insecure {
		scheme = "http"
	}

	return scheme + "://" + ep.hostPort + ep.path + "/v1/" + signal
}

// OTLPConfig configures the OTLP exporters. The endpoint, protocol, headers
// and TLS settings are shared by every signal. The standard OTEL_* environment
// variables override the file; the signal specific OTEL_EXPORTER_OTLP_TRACES_*,
// OTEL_EXPORTER_OTLP_METRICS_* and OTEL_EXPORTER_OTLP_LOGS_* variables only
// apply while endpoint is empty.
type OTLPConfig struct {
	// Enabled is the master switch: nothing is exported without it, whatever
	// the per-signal switches say. Off by default; OTEL_SDK_DISABLED=true
	// forces it off.
	Enabled bool `yaml:"enabled"`
	// Endpoint is host:port or an http(s) URL of the collector. Empty uses the
	// SDK default (localhost:4317 for grpc, localhost:4318 for http).
	Endpoint string `yaml:"endpoint"`
	// Protocol is "grpc" (default) or "http" (OTLP/HTTP protobuf).
	Protocol string `yaml:"protocol"`
	// Headers are sent with every export request (collector credentials).
	// Never logged. The environment form is comma-separated key=value pairs
	// with percent-encoded values.
	Headers map[string]string `yaml:"headers"`
	// Insecure disables TLS towards the collector. An http:// endpoint implies it.
	Insecure bool `yaml:"insecure"`
	// ServiceName is the service.name resource attribute.
	ServiceName string `yaml:"serviceName"`
	// SampleRatio is the fraction of new traces recorded, in [0,1]. Requests
	// carrying a sampled traceparent are always recorded (parent based).
	SampleRatio float64 `yaml:"sampleRatio"`
	// Traces exports distributed traces and installs the HTTP server
	// middleware. On by default, so enabled alone keeps tracing. The
	// environment form is OTEL_TRACES_EXPORTER: otlp turns it on, none turns
	// it off.
	Traces bool `yaml:"traces"`
	// Metrics exports the Prometheus registry (Go runtime and openartifact_*
	// families) over OTLP to the same endpoint. /metrics is unaffected. The
	// environment form is OTEL_METRICS_EXPORTER: otlp turns it on, none turns
	// it off.
	Metrics bool `yaml:"metrics"`
	// Logs sends the application log records over OTLP as well, next to the
	// configured log output; with it (or audit) on, an OTLP log provider is
	// started on the same endpoint. The environment form is
	// OTEL_LOGS_EXPORTER: otlp turns it on, none turns it off.
	Logs bool `yaml:"logs"`
	// Audit is the switch for audit events over OTLP (scope
	// openartifact.audit), independent of traces. It shares the log provider
	// with logs. Reserved: no events are written to it yet (#193). The
	// audit_logs table stays the source of truth.
	Audit bool `yaml:"audit"`
}

// anySignal reports whether at least one signal is switched on.
func (c OTLPConfig) anySignal() bool { return c.Traces || c.Metrics || c.Logs || c.Audit }

// DefaultConfig returns the defaults: disabled, grpc, traces only, every trace
// sampled.
func DefaultConfig() Config {
	return Config{OTLP: OTLPConfig{
		Traces:      true,
		Protocol:    ProtocolGRPC,
		ServiceName: DefaultServiceName,
		SampleRatio: 1,
		Headers:     map[string]string{},
	}}
}

// NormalizedProtocol maps the accepted spellings to ProtocolGRPC/ProtocolHTTP.
// ok is false for anything else.
func (c OTLPConfig) NormalizedProtocol() (string, bool) {
	switch strings.ToLower(strings.TrimSpace(c.Protocol)) {
	case "", ProtocolGRPC:
		return ProtocolGRPC, true
	case ProtocolHTTP, "http/protobuf":
		return ProtocolHTTP, true
	}

	return "", false
}

// Validate checks the settings. A disabled configuration is always valid so a
// stale value never blocks startup.
func (c Config) Validate() error {
	o := c.OTLP
	if !o.Enabled {
		return nil
	}

	if _, ok := o.NormalizedProtocol(); !ok {
		return errProtocol
	}

	if math.IsNaN(o.SampleRatio) || o.SampleRatio < 0 || o.SampleRatio > 1 {
		return errSampleRatio
	}

	for k := range o.Headers {
		if strings.TrimSpace(k) == "" {
			return errHeaderName
		}
	}

	if o.Endpoint != "" {
		if _, err := parseEndpoint(o.Endpoint); err != nil {
			return err
		}
	}

	if c.Frontend.Enabled {
		return c.Frontend.validate(o)
	}

	return nil
}

// validate checks that the relay has an OTLP/HTTP target: its own endpoint,
// or the shared one when that speaks OTLP/HTTP.
func (f FrontendConfig) validate(o OTLPConfig) error {
	if f.Endpoint != "" {
		if ep, err := parseEndpoint(f.Endpoint); err != nil || !strings.Contains(f.Endpoint, "://") || ep.hostPort == "" {
			return errFrontendEndpoint
		}

		return nil
	}

	if proto, _ := o.NormalizedProtocol(); proto != ProtocolHTTP || o.Endpoint == "" {
		return errFrontendNoTarget
	}

	return nil
}

// endpoint is a parsed collector address.
type endpoint struct {
	hostPort string
	// path is the URL path prefix (OTLP/HTTP only), without the signal suffix.
	path string
	// plaintext is true for an http:// URL.
	plaintext bool
}

func parseEndpoint(raw string) (endpoint, error) {
	raw = strings.TrimSpace(raw)

	if !strings.Contains(raw, "://") {
		if raw == "" || strings.ContainsAny(raw, "/?#@ ") {
			return endpoint{}, errEndpoint
		}

		return endpoint{hostPort: raw}, nil
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return endpoint{}, fmt.Errorf("%w", errEndpoint)
	}

	return endpoint{
		hostPort:  u.Host,
		path:      strings.TrimRight(u.Path, "/"),
		plaintext: u.Scheme == "http",
	}, nil
}
