// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// LogFields returns the "trace_id" and "span_id" key/value pairs of the span in
// ctx, ready to append to a zap sugared call, or nil when ctx has no valid
// span (telemetry disabled, or a request outside any trace). The ids let a log
// line be joined to its trace in the backend.
func LogFields(ctx context.Context) []any {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}

	return []any{"trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String()}
}
