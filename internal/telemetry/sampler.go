// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"
	"math/rand/v2"
	"net/http"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// sessionCookieName is the web session cookie (auth.SessionCookieName; the auth
// package imports this one, so the name is repeated and a test pins the two
// together).
const sessionCookieName = "oa_session"

type credentialedKey struct{}

// withCredentials marks ctx as belonging to a request that carries
// credentials.
func withCredentials(ctx context.Context) context.Context {
	return context.WithValue(ctx, credentialedKey{}, true)
}

func credentialed(ctx context.Context) bool {
	v, _ := ctx.Value(credentialedKey{}).(bool)

	return v
}

// hasCredentials reports whether r presents anything the server would try to
// authenticate: an Authorization header (Bearer, Basic, tokens), an API key
// header or the session cookie. Nothing is validated here, the check only
// separates the callers that can be known at span start from the anonymous
// ones; the server span starts before the router, hence before authentication.
func hasCredentials(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
		return true
	}

	c, err := r.Cookie(sessionCookieName)

	return err == nil && c.Value != ""
}

// inboundSampler is the tracer provider's sampler. Spans with a local parent
// and requests that carry credentials follow their parent (parent based): a CI
// job, a proxy or the web UI that sampled a trace gets it recorded. A request
// that arrives with a remote parent and no credentials does not: the caller is
// anonymous, so its sampled flag is not honored, and the decision is a fresh
// draw at ratio, which also does not trust the caller's trace id (a client
// could pick ids that a trace-id ratio sampler always accepts). Without this
// anyone could force every anonymous request to be recorded and exported by
// sending a sampled traceparent.
type inboundSampler struct {
	parentBased sdktrace.Sampler
	ratio       float64
}

func newInboundSampler(ratio float64) sdktrace.Sampler {
	var root sdktrace.Sampler

	switch {
	case ratio >= 1:
		root = sdktrace.AlwaysSample()
	case ratio <= 0:
		root = sdktrace.NeverSample()
	default:
		root = sdktrace.TraceIDRatioBased(ratio)
	}

	return &inboundSampler{parentBased: sdktrace.ParentBased(root), ratio: ratio}
}

func (s *inboundSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	sc := trace.SpanContextFromContext(p.ParentContext)
	if !sc.IsValid() || !sc.IsRemote() || credentialed(p.ParentContext) {
		return s.parentBased.ShouldSample(p)
	}

	res := sdktrace.SamplingResult{Decision: sdktrace.Drop, Tracestate: sc.TraceState()}
	if s.ratio >= 1 || (s.ratio > 0 && rand.Float64() < s.ratio) { //nolint:gosec // sampling draw, not security
		res.Decision = sdktrace.RecordAndSample
	}

	return res
}

func (s *inboundSampler) Description() string {
	return "InboundSampler{" + s.parentBased.Description() + "}"
}
