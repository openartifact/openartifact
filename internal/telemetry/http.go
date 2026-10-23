// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// outboundPropagator is what client requests carry: W3C trace context only.
// The process-wide propagator also extracts baggage from incoming requests,
// which is deliberately not forwarded.
var outboundPropagator propagation.TextMapPropagator = propagation.TraceContext{} //nolint:gochecknoglobals // stateless

// methodOther replaces request methods outside the standard set so a client
// cannot mint span names or attribute values.
const methodOther = "_OTHER"

func normalizeMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
		http.MethodConnect, http.MethodTrace:
		return m
	}

	return methodOther
}

// RoutePattern returns the chi route template of the request being served
// ("/repos/{name}/*" becomes "/repos/{name}"), or "" when no route matched.
// It is only complete once the router has dispatched the request.
func RoutePattern(ctx context.Context) string {
	rctx := chi.RouteContext(ctx)
	if rctx == nil {
		return ""
	}

	return strings.TrimSuffix(rctx.RoutePattern(), "/*")
}

// SpanName is the server span name for a request: "<METHOD> <route template>",
// or just the method when no route matched. The raw path never appears, so the
// number of distinct names is bounded by the route table.
func SpanName(method, route string) string {
	if route == "" {
		return method
	}

	return method + " " + route
}

// HTTPMiddleware starts a server span per request, continuing the W3C
// traceparent/baggage of the caller. The span is renamed to the matched route
// template once the router has run (chi only knows it afterwards). It returns
// next untouched while telemetry is disabled.
//
// It wraps the router from outside (api.HTTPServer.AddMiddleware), where chi's
// route context does not exist yet, so it seeds one: chi reuses it, and the
// matched pattern is readable here after next returns. Mount it before the
// logging middleware so log lines can carry the trace id. Probe endpoints
// (/health, /ready, /metrics) are not traced: polled every few seconds they
// would drown real traffic.
func HTTPMiddleware() func(http.Handler) http.Handler {
	if !Enabled() {
		return func(next http.Handler) http.Handler { return next }
	}

	tracer := otel.Tracer(scope)
	prop := otel.GetTextMapPropagator()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) {
				next.ServeHTTP(w, r)

				return
			}

			ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			if chi.RouteContext(ctx) == nil {
				ctx = context.WithValue(ctx, chi.RouteCtxKey, chi.NewRouteContext())
			}

			method := normalizeMethod(r.Method)

			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}

			//nolint:spancheck // ended by finishServer in the deferred function below
			ctx, span := tracer.Start(ctx, method,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", method),
					attribute.String("url.scheme", scheme),
					attribute.String("network.protocol.version", protoVersion(r)),
				))

			rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				// A panic bubbles up to the recovery middleware, which answers
				// 500; record that instead of the optimistic 200.
				if p := recover(); p != nil {
					rw.status = http.StatusInternalServerError
					finishServer(ctx, span, method, rw.status)
					panic(p)
				}

				finishServer(ctx, span, method, rw.status)
			}()

			next.ServeHTTP(rw, r.WithContext(ctx))
		}) //nolint:spancheck // span ended by the deferred finishServer
	}
}

func isProbe(path string) bool {
	return path == "/health" || path == "/ready" || path == "/metrics"
}

func finishServer(ctx context.Context, span trace.Span, method string, status int) {
	route := RoutePattern(ctx)
	span.SetName(SpanName(method, route))

	attrs := []attribute.KeyValue{attribute.Int("http.response.status_code", status)}
	if route != "" {
		attrs = append(attrs, attribute.String("http.route", route))
	}

	span.SetAttributes(attrs...)

	// Server spans are errors only for 5xx: a 404 or 401 is the client's.
	if status >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, "")
	}

	span.End()
}

func protoVersion(r *http.Request) string {
	return strconv.Itoa(r.ProtoMajor) + "." + strconv.Itoa(r.ProtoMinor)
}

// statusWriter records the status code the handler chose.
type statusWriter struct {
	http.ResponseWriter

	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush keeps streaming handlers working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// WrapClient instruments c in place: every request through it becomes a client
// span ("HTTP <METHOD>") and carries the traceparent/tracestate headers (never baggage) of the
// calling context. Attributes are limited to method, scheme, host and port
// (never the path or query, which may hold tokens or presigned signatures) and
// the status code. It is a no-op at request time while telemetry is disabled.
// The client's redirect policy and timeout are untouched.
func WrapClient(c *http.Client) *http.Client {
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	c.Transport = &transport{base: base}

	return c
}

type transport struct{ base http.RoundTripper }

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !Enabled() {
		return t.base.RoundTrip(req)
	}

	method := normalizeMethod(req.Method)
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", method),
		attribute.String("url.scheme", req.URL.Scheme),
		attribute.String("server.address", req.URL.Hostname()),
	}

	if p := req.URL.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			attrs = append(attrs, attribute.Int("server.port", n))
		}
	}

	ctx, span := otel.Tracer(scope).Start(req.Context(), "HTTP "+method,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))

	// RoundTrippers must not mutate the caller's request.
	req = req.Clone(ctx)
	// Trace context only: baggage comes from untrusted clients and must not
	// reach upstreams, webhook endpoints or AI providers.
	outboundPropagator.Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		span.SetAttributes(attribute.String("error.type", errorType(err)))
		span.SetStatus(codes.Error, "")
		span.End()

		return nil, err
	}

	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))

	if resp.StatusCode >= http.StatusBadRequest {
		span.SetAttributes(attribute.String("error.type", strconv.Itoa(resp.StatusCode)))
		span.SetStatus(codes.Error, "")
	}

	// End with the body so the span covers the transfer of large downloads.
	resp.Body = &endOnClose{ReadCloser: resp.Body, span: span}

	return resp, nil
}

// errorType names the error's Go type. url.Error is unwrapped: its text embeds
// the request URL, which can carry credentials.
func errorType(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		err = ue.Err
	}

	return typeName(err)
}

type endOnClose struct {
	io.ReadCloser

	span trace.Span
	once sync.Once
}

func (b *endOnClose) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.end()
	}

	return n, err
}

func (b *endOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.end()

	return err
}

func (b *endOnClose) end() { b.once.Do(func() { b.span.End() }) }

// typeName is the Go type of err, a bounded value fit for error.type.
func typeName(err error) string { return fmt.Sprintf("%T", err) }
