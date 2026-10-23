// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telemetry wires OpenTelemetry: the OTLP exporters (traces, plus an
// optional bridge that ships the Prometheus registry as OTLP metrics), the
// HTTP server middleware, an instrumented client transport and a pgx tracer.
//
// Everything is off unless Config.OTLP.Enabled is set: no provider is
// installed, no middleware is added and the transport and pgx tracer reduce to
// an atomic load, so a disabled build behaves as it did before.
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
	errProtocol    = errors.New("telemetry.otlp.protocol must be grpc, http or http/protobuf")
	errSampleRatio = errors.New("telemetry.otlp.sampleRatio must be between 0 and 1")
	errEndpoint    = errors.New("telemetry.otlp.endpoint must be host:port or an http(s) URL without query or fragment")
	errHeaderName  = errors.New("telemetry.otlp.headers must not contain an empty header name")
)

// Config is the telemetry section of the server configuration.
type Config struct {
	OTLP OTLPConfig `yaml:"otlp"`
}

// OTLPConfig configures the OTLP exporters. The standard OTEL_* environment
// variables override the file (see internal/config for the mapping); signal
// specific OTEL_EXPORTER_OTLP_TRACES_* variables only apply while Endpoint is
// empty.
type OTLPConfig struct {
	// Enabled turns tracing on. Off by default; OTEL_SDK_DISABLED=true forces
	// it off.
	Enabled bool `yaml:"enabled"`
	// Endpoint is host:port or an http(s) URL of the collector. Empty uses the
	// SDK default (localhost:4317 for grpc, localhost:4318 for http).
	Endpoint string `yaml:"endpoint"`
	// Protocol is "grpc" (default) or "http" (OTLP/HTTP protobuf).
	Protocol string `yaml:"protocol"`
	// Headers are sent with every export request (collector credentials).
	// Never logged.
	Headers map[string]string `yaml:"headers"`
	// Insecure disables TLS towards the collector. An http:// endpoint implies it.
	Insecure bool `yaml:"insecure"`
	// ServiceName is the service.name resource attribute.
	ServiceName string `yaml:"serviceName"`
	// SampleRatio is the fraction of new traces recorded, in [0,1]. Requests
	// carrying a sampled traceparent are always recorded (parent based).
	SampleRatio float64 `yaml:"sampleRatio"`
	// Metrics additionally exports the Prometheus registry (Go runtime and
	// openartifact_* families) over OTLP to the same endpoint. /metrics is
	// unaffected.
	Metrics bool `yaml:"metrics"`
}

// DefaultConfig returns the defaults: disabled, grpc, every trace sampled.
func DefaultConfig() Config {
	return Config{OTLP: OTLPConfig{
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
