// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const sampledParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// serveSampled sends a request carrying a sampled traceparent and the extra
// headers/cookies through the middleware, under a tracer provider that uses the
// sampler Setup installs for ratio, and reports how many server spans were
// recorded and whether the handler saw a sampled span.
func serveSampled(t *testing.T, ratio float64, mutate func(*http.Request)) (int, bool) {
	t.Helper()

	var sampled bool

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(rec),
		sdktrace.WithSampler(telemetry.Sampler(ratio)),
	)
	t.Cleanup(telemetry.Install(tp))

	h := telemetry.HTTPMiddleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sampled = trace.SpanContextFromContext(r.Context()).IsSampled()
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	req.Header.Set("Traceparent", sampledParent)

	if mutate != nil {
		mutate(req)
	}

	h.ServeHTTP(httptest.NewRecorder(), req)

	return len(rec.Ended()), sampled
}

// TestAnonymousSampledTraceparentDoesNotForceRecording: with sampling off, a
// client-sent sampled traceparent records nothing unless the request carries
// credentials.
func TestAnonymousSampledTraceparentDoesNotForceRecording(t *testing.T) {
	if n, sampled := serveSampled(t, 0, nil); n != 0 || sampled {
		t.Errorf("anonymous request with sampled traceparent: %d spans recorded, sampled=%v; want none", n, sampled)
	}

	creds := map[string]func(*http.Request){
		"bearer":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer t") },
		"api key": func(r *http.Request) { r.Header.Set("X-Api-Key", "k") },
		"session": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "s"}) },
	}
	for name, mutate := range creds {
		if n, sampled := serveSampled(t, 0, mutate); n != 1 || !sampled {
			t.Errorf("%s request with sampled traceparent: %d spans, sampled=%v; want the parent's decision honored", name, n, sampled)
		}
	}
}

// TestAnonymousSamplingIsRatioNotTraceID: a client that picks trace ids a
// trace-id ratio sampler always accepts (all zero low bits) still gets only the
// configured fraction.
func TestAnonymousSamplingIsRatioNotTraceID(t *testing.T) {
	s := telemetry.Sampler(0.5)

	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{15: 1}, // lowest valid id: TraceIDRatioBased accepts it at any ratio > 0
		SpanID:     trace.SpanID{1},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), remote)

	const n = 2000

	sampled := 0

	for range n {
		res := s.ShouldSample(sdktrace.SamplingParameters{ParentContext: ctx, TraceID: remote.TraceID(), Name: "x"})
		if res.Decision == sdktrace.RecordAndSample {
			sampled++
		}
	}

	if sampled == 0 || sampled == n {
		t.Errorf("%d of %d anonymous requests sampled at ratio 0.5; want a mix", sampled, n)
	}
}

func TestLocalChildFollowsParent(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(telemetry.Sampler(0)))
	tr := tp.Tracer("t")

	// A root at ratio 0 is not sampled, and neither is its child.
	ctx, root := tr.Start(context.Background(), "root")
	_, child := tr.Start(ctx, "child")

	if root.SpanContext().IsSampled() || child.SpanContext().IsSampled() {
		t.Error("spans sampled at ratio 0")
	}

	// A credentialed remote parent is followed, so a sampled child of a sampled
	// local span stays sampled.
	on := sdktrace.NewTracerProvider(sdktrace.WithSampler(telemetry.Sampler(1))).Tracer("t")
	ctx, root = on.Start(context.Background(), "root")
	_, child = on.Start(ctx, "child")

	if !root.SpanContext().IsSampled() || !child.SpanContext().IsSampled() {
		t.Error("spans not sampled at ratio 1")
	}
}

// TestSessionCookieName pins the cookie name the sampler checks to the one the
// session code sets (the telemetry package cannot import auth).
func TestSessionCookieName(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "s"})

	if !telemetry.HasCredentials(r) {
		t.Errorf("a request with the %q cookie is not credentialed", auth.SessionCookieName)
	}
}
