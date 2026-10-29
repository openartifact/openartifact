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
// disables it). logDropped counts, by instrumentation scope, the log records
// the log provider drops because its export queue is full (nil counts
// nothing). The shutdown flushes every provider and restores the no-op ones.
func Setup(
	ctx context.Context,
	cfg Config,
	build Build,
	gatherer prometheus.Gatherer,
	logDropped *prometheus.CounterVec,
	log Logger,
) (func(context.Context) error, error) {
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

	stops, err := startProviders(ctx, o, res, gatherer, logDropped)
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
func startProviders(
	ctx context.Context,
	o OTLPConfig,
	res *resource.Resource,
	gatherer prometheus.Gatherer,
	logDropped *prometheus.CounterVec,
) ([]func(context.Context) error, error) {
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

		stops = append(stops, shutdownWith(tp, Install(tp)))
	}

	if o.Metrics && gatherer != nil {
		mp, err := newMeterProvider(ctx, o, res, gatherer)
		if err != nil {
			return stops, fmt.Errorf("telemetry metric exporter: %w", err)
		}

		otel.SetMeterProvider(mp)

		stops = append(stops, shutdownWith(mp, func() { otel.SetMeterProvider(metricnoop.NewMeterProvider()) }))
	}

	// One log provider serves both the application (logs) and the
	// openartifact.audit (audit) scopes; which scopes write to it is up to
	// their emitters, according to the two switches.
	if o.Logs || o.Audit {
		lp, err := newLoggerProvider(ctx, o, res, logDropped)
		if err != nil {
			return stops, fmt.Errorf("telemetry log exporter: %w", err)
		}

		otel.SetLoggerProvider(lp)

		stops = append(stops, shutdownWith(lp, func() { otel.SetLoggerProvider(lognoop.NewLoggerProvider()) }))
	}

	return stops, nil
}

// shutdownWith returns the stop function of an installed provider: it runs
// uninstall (which restores the global no-op provider), then shuts p down.
func shutdownWith[P interface{ Shutdown(context.Context) error }](p P, uninstall func()) func(context.Context) error {
	return func(ctx context.Context) error {
		uninstall()

		return p.Shutdown(ctx)
	}
}

// sampler is the trace sampler for sample ratio ratio (see inboundSampler).
func sampler(ratio float64) sdktrace.Sampler { return newInboundSampler(ratio) }

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
		return otlptracehttp.New(ctx, exporterOptions(o, ep, "/v1/traces",
			otlptracehttp.WithEndpoint, otlptracehttp.WithURLPath, otlptracehttp.WithInsecure, otlptracehttp.WithHeaders)...)
	}

	return otlptracegrpc.New(ctx, exporterOptions(o, ep, "",
		otlptracegrpc.WithEndpoint, nil, otlptracegrpc.WithInsecure, otlptracegrpc.WithHeaders)...)
}

// exporterOptions builds the options of an OTLP exporter from the
// configuration. The trace, metric and log exporter packages (HTTP and gRPC
// alike) each have their own Option type, so their constructors come in as
// arguments: withURLPath is nil for gRPC, which has no URL path, and the HTTP
// path is the endpoint's prefix followed by signalPath.
func exporterOptions[O any](
	o OTLPConfig,
	ep endpoint,
	signalPath string,
	withEndpoint, withURLPath func(string) O,
	withInsecure func() O,
	withHeaders func(map[string]string) O,
) []O {
	var opts []O

	if ep.hostPort != "" {
		opts = append(opts, withEndpoint(ep.hostPort))

		if withURLPath != nil {
			opts = append(opts, withURLPath(ep.path+signalPath))
		}
	}

	if o.Insecure || ep.plaintext {
		opts = append(opts, withInsecure())
	}

	if len(o.Headers) > 0 {
		opts = append(opts, withHeaders(o.Headers))
	}

	return opts
}

func newMeterProvider(ctx context.Context, o OTLPConfig, res *resource.Resource, g prometheus.Gatherer) (*sdkmetric.MeterProvider, error) {
	ep, err := endpointFor(o)
	if err != nil {
		return nil, err
	}

	var exp sdkmetric.Exporter

	proto, _ := o.NormalizedProtocol()

	if proto == ProtocolHTTP {
		exp, err = otlpmetrichttp.New(ctx, exporterOptions(o, ep, "/v1/metrics",
			otlpmetrichttp.WithEndpoint, otlpmetrichttp.WithURLPath, otlpmetrichttp.WithInsecure, otlpmetrichttp.WithHeaders)...)
	} else {
		exp, err = otlpmetricgrpc.New(ctx, exporterOptions(o, ep, "",
			otlpmetricgrpc.WithEndpoint, nil, otlpmetricgrpc.WithInsecure, otlpmetricgrpc.WithHeaders)...)
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

func newLoggerProvider(
	ctx context.Context,
	o OTLPConfig,
	res *resource.Resource,
	dropped *prometheus.CounterVec,
) (*sdklog.LoggerProvider, error) {
	ep, err := endpointFor(o)
	if err != nil {
		return nil, err
	}

	var exp sdklog.Exporter

	proto, _ := o.NormalizedProtocol()

	if proto == ProtocolHTTP {
		exp, err = otlploghttp.New(ctx, exporterOptions(o, ep, "/v1/logs",
			otlploghttp.WithEndpoint, otlploghttp.WithURLPath, otlploghttp.WithInsecure, otlploghttp.WithHeaders)...)
	} else {
		exp, err = otlploggrpc.New(ctx, exporterOptions(o, ep, "",
			otlploggrpc.WithEndpoint, nil, otlploggrpc.WithInsecure, otlploggrpc.WithHeaders)...)
	}

	if err != nil {
		return nil, err
	}

	return sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(newCountingProcessor(exp, dropped)),
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
