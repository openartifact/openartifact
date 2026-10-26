// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	promBridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// scope is the instrumentation scope name of every span created here.
const scope = "github.com/openartifact/openartifact"

// errorLogInterval rate-limits the exporter error log: a dead collector fails
// every batch.
const errorLogInterval = time.Minute

// installed reports whether a tracer provider has been installed. Hot paths
// (client transport, pgx tracer) test it first so a disabled build pays one
// atomic load.
var installed atomic.Bool //nolint:gochecknoglobals // process-wide on/off switch, set once at startup

// Enabled reports whether tracing is active.
func Enabled() bool { return installed.Load() }

// Logger is the subset of the application logger used to report exporter
// failures.
type Logger interface {
	Warnw(msg string, keysAndValues ...any)
}

// Build identifies the running server in the exported resource.
type Build struct {
	Version    string
	InstanceID string
}

// Install makes tp the process tracer provider and enables W3C trace context
// and baggage propagation. The returned function restores the disabled state.
// Setup uses it; tests use it with an in-memory provider.
func Install(tp trace.TracerProvider) func() {
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	installed.Store(true)

	return func() {
		installed.Store(false)
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	}
}

// Setup starts one provider per signal switched on in cfg and installs it as
// the process-wide provider: the tracer provider (with Install, so Enabled and
// HTTPMiddleware follow it) for traces, the meter provider for metrics, and a
// single log provider (otel.SetLoggerProvider) when logs or audit is on. With
// cfg.OTLP.Enabled false, or every signal off, it installs nothing and the
// returned shutdown is a no-op. gatherer feeds the metrics bridge (nil
// disables it). The shutdown flushes every provider and restores the no-op
// ones.
func Setup(ctx context.Context, cfg Config, build Build, gatherer prometheus.Gatherer, log Logger) (func(context.Context) error, error) {
	noopShutdown := func(context.Context) error { return nil }

	o := cfg.OTLP
	if !o.Enabled {
		return noopShutdown, nil
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if !o.anySignal() {
		if log != nil {
			log.Warnw("telemetry.otlp.enabled is set but every signal (traces, metrics, logs, audit) is off: nothing is exported")
		}

		return noopShutdown, nil
	}

	res, err := newResource(ctx, o, build)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	otel.SetErrorHandler(newErrorHandler(log))

	stops, err := startProviders(ctx, o, res, gatherer)
	shutdown := func(ctx context.Context) error {
		var errs []error
		for _, s := range stops {
			errs = append(errs, s(ctx))
		}

		return errors.Join(errs...)
	}

	if err != nil {
		_ = shutdown(ctx)

		return nil, err
	}

	return shutdown, nil
}

// startProviders creates and installs the provider of every enabled signal.
// It returns the stop function of each one installed so far, also on error.
func startProviders(ctx context.Context, o OTLPConfig, res *resource.Resource, gatherer prometheus.Gatherer) ([]func(context.Context) error, error) {
	var stops []func(context.Context) error

	if o.Traces {
		exp, err := newTraceExporter(ctx, o)
		if err != nil {
			return stops, fmt.Errorf("telemetry trace exporter: %w", err)
		}

		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sampler(o.SampleRatio)),
		)
		uninstall := Install(tp)

		stops = append(stops, func(ctx context.Context) error {
			uninstall()

			return tp.Shutdown(ctx)
		})
	}

	if o.Metrics && gatherer != nil {
		mp, err := newMeterProvider(ctx, o, res, gatherer)
		if err != nil {
			return stops, fmt.Errorf("telemetry metric exporter: %w", err)
		}

		otel.SetMeterProvider(mp)

		stops = append(stops, func(ctx context.Context) error {
			otel.SetMeterProvider(metricnoop.NewMeterProvider())

			return mp.Shutdown(ctx)
		})
	}

	// One log provider serves both the application (logs) and the
	// openartifact.audit (audit) scopes; which scopes write to it is up to
	// their emitters, according to the two switches.
	if o.Logs || o.Audit {
		lp, err := newLoggerProvider(ctx, o, res)
		if err != nil {
			return stops, fmt.Errorf("telemetry log exporter: %w", err)
		}

		otel.SetLoggerProvider(lp)

		stops = append(stops, func(ctx context.Context) error {
			otel.SetLoggerProvider(lognoop.NewLoggerProvider())

			return lp.Shutdown(ctx)
		})
	}

	return stops, nil
}

func sampler(ratio float64) sdktrace.Sampler {
	switch {
	case ratio >= 1:
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case ratio <= 0:
		return sdktrace.ParentBased(sdktrace.NeverSample())
	}

	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

func newResource(ctx context.Context, o OTLPConfig, b Build) (*resource.Resource, error) {
	name := o.ServiceName
	if name == "" {
		name = DefaultServiceName
	}

	attrs := []attribute.KeyValue{attribute.String("service.name", name)}
	if b.Version != "" {
		attrs = append(attrs, attribute.String("service.version", b.Version))
	}

	if b.InstanceID != "" {
		attrs = append(attrs, attribute.String("service.instance.id", b.InstanceID))
	}

	// OTEL_RESOURCE_ATTRIBUTES is read after the explicit attributes so an
	// operator can add or override resource attributes without a rebuild.
	return resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
}

func newTraceExporter(ctx context.Context, o OTLPConfig) (sdktrace.SpanExporter, error) {
	ep, err := endpointFor(o)
	if err != nil {
		return nil, err
	}

	proto, _ := o.NormalizedProtocol()

	if proto == ProtocolHTTP {
		opts := []otlptracehttp.Option{}
		if ep.hostPort != "" {
			opts = append(opts, otlptracehttp.WithEndpoint(ep.hostPort), otlptracehttp.WithURLPath(ep.path+"/v1/traces"))
		}

		if o.Insecure || ep.plaintext {
			opts = append(opts, otlptracehttp.WithInsecure())
		}

		if len(o.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(o.Headers))
		}

		return otlptracehttp.New(ctx, opts...)
	}

	opts := []otlptracegrpc.Option{}
	if ep.hostPort != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(ep.hostPort))
	}

	if o.Insecure || ep.plaintext {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	if len(o.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(o.Headers))
	}

	return otlptracegrpc.New(ctx, opts...)
}

func newMeterProvider(ctx context.Context, o OTLPConfig, res *resource.Resource, g prometheus.Gatherer) (*sdkmetric.MeterProvider, error) {
	ep, err := endpointFor(o)
	if err != nil {
		return nil, err
	}

	var exp sdkmetric.Exporter

	proto, _ := o.NormalizedProtocol()

	if proto == ProtocolHTTP {
		opts := []otlpmetrichttp.Option{}
		if ep.hostPort != "" {
			opts = append(opts, otlpmetrichttp.WithEndpoint(ep.hostPort), otlpmetrichttp.WithURLPath(ep.path+"/v1/metrics"))
		}

		if o.Insecure || ep.plaintext {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}

		if len(o.Headers) > 0 {
			opts = append(opts, otlpmetrichttp.WithHeaders(o.Headers))
		}

		exp, err = otlpmetrichttp.New(ctx, opts...)
	} else {
		opts := []otlpmetricgrpc.Option{}
		if ep.hostPort != "" {
			opts = append(opts, otlpmetricgrpc.WithEndpoint(ep.hostPort))
		}

		if o.Insecure || ep.plaintext {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}

		if len(o.Headers) > 0 {
			opts = append(opts, otlpmetricgrpc.WithHeaders(o.Headers))
		}

		exp, err = otlpmetricgrpc.New(ctx, opts...)
	}

	if err != nil {
		return nil, err
	}

	// The bridge reads the Prometheus registry on every collection, so the
	// existing instruments are exported unchanged (no second set of meters).
	producer := promBridge.NewMetricProducer(promBridge.WithGatherer(g))

	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithProducer(producer))),
	), nil
}

func newLoggerProvider(ctx context.Context, o OTLPConfig, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	ep, err := endpointFor(o)
	if err != nil {
		return nil, err
	}

	var exp sdklog.Exporter

	proto, _ := o.NormalizedProtocol()

	if proto == ProtocolHTTP {
		opts := []otlploghttp.Option{}
		if ep.hostPort != "" {
			opts = append(opts, otlploghttp.WithEndpoint(ep.hostPort), otlploghttp.WithURLPath(ep.path+"/v1/logs"))
		}

		if o.Insecure || ep.plaintext {
			opts = append(opts, otlploghttp.WithInsecure())
		}

		if len(o.Headers) > 0 {
			opts = append(opts, otlploghttp.WithHeaders(o.Headers))
		}

		exp, err = otlploghttp.New(ctx, opts...)
	} else {
		opts := []otlploggrpc.Option{}
		if ep.hostPort != "" {
			opts = append(opts, otlploggrpc.WithEndpoint(ep.hostPort))
		}

		if o.Insecure || ep.plaintext {
			opts = append(opts, otlploggrpc.WithInsecure())
		}

		if len(o.Headers) > 0 {
			opts = append(opts, otlploggrpc.WithHeaders(o.Headers))
		}

		exp, err = otlploggrpc.New(ctx, opts...)
	}

	if err != nil {
		return nil, err
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	), nil
}

// endpointFor parses the configured endpoint; the zero value (unset) leaves
// the choice to the SDK defaults and its OTEL_EXPORTER_OTLP_* variables.
func endpointFor(o OTLPConfig) (endpoint, error) {
	if o.Endpoint == "" {
		return endpoint{}, nil
	}

	return parseEndpoint(o.Endpoint)
}

// newErrorHandler routes SDK/exporter errors to the application log, at most
// once per errorLogInterval (a dead collector fails every batch). Only the
// error text is logged: exporter errors carry the collector address and gRPC
// status, never the configured headers.
func newErrorHandler(log Logger) otel.ErrorHandler {
	var (
		mu   sync.Mutex
		last time.Time
	)

	return otel.ErrorHandlerFunc(func(err error) {
		if log == nil || err == nil {
			return
		}

		mu.Lock()
		now := time.Now()
		due := now.Sub(last) >= errorLogInterval

		if due {
			last = now
		}
		mu.Unlock()

		if due {
			log.Warnw("OpenTelemetry export error", "error", err)
		}
	})
}
