// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap/zapcore"
)

// LogFields returns the "trace_id" and "span_id" key/value pairs of the span in
// ctx, ready to append to a zap sugared call, or nil when ctx has no valid
// span (telemetry disabled, or a request outside any trace). The ids let a log
// line be joined to its trace in the backend.
//
// The pairs are followed by ctx itself, in a field no encoder writes: the log
// line is unchanged, and the OTLP bridge (LogCore) emits the record under ctx,
// so the record carries the span's trace and span ids as its own, not only as
// attributes.
func LogFields(ctx context.Context) []any {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}

	return []any{
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
		zapcore.Field{Key: "context", Type: zapcore.SkipType, Interface: ctx},
	}
}

// LogCore returns the zap core that bridges application log records to the
// log provider Setup installs, under the application instrumentation scope.
// The provider maps the zap level to the record severity and adds the service.*
// resource attributes; a context field (see LogFields) sets the trace and span
// ids.
func LogCore() zapcore.Core { return otelzap.NewCore(scope) }
