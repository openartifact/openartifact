// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// StartSpan opens an internal span named name under a sampled request span and
// returns the context carrying it plus the function that ends it (pass the
// operation's error, or nil). It exists to break a request into steps that
// are not database queries (object store calls, for instance), so name and
// attrs must come from a fixed set: never identifiers, paths or URLs. A
// failed span records the Go type of the error, never its message.
//
// While telemetry is disabled or the request is not sampled it returns ctx
// and a no-op, so it is safe on hot paths.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(err error)) {
	if !Enabled() || !trace.SpanFromContext(ctx).IsRecording() {
		return ctx, func(error) {}
	}

	//nolint:spancheck // ended by the returned function
	ctx, span := otel.Tracer(scope).Start(ctx, name, trace.WithAttributes(attrs...))

	//nolint:spancheck // the closure ends the span
	return ctx, func(err error) {
		if err != nil {
			span.SetAttributes(attribute.String("error.type", typeName(err)))
			span.SetStatus(codes.Error, "")
		}

		span.End()
	}
}
