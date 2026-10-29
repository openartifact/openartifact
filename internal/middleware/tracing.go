// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"go.opentelemetry.io/otel/trace"
)

type contextKey string

const TraceIDKey contextKey = "trace_id"

// TracingMiddleware generates a trace ID per request if not present in headers
// (only a 32-character lowercase hex one is accepted from the client, anything
// else is replaced: the value is echoed in a response header and written to the
// logs) and propagates it via X-Trace-ID response header. When OpenTelemetry is
// enabled the request has a W3C trace (telemetry.HTTPMiddleware runs first) and
// its trace id is used instead, so the header, the logs and the exported spans
// agree.
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-Id")
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			traceID = sc.TraceID().String()
		} else if !validTraceID(traceID) {
			traceID = generateTraceID()
		}

		ctx := context.WithValue(r.Context(), TraceIDKey, traceID)

		w.Header().Set("X-Trace-Id", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// generateTraceID generates a random 16-byte hex trace ID.
func generateTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// traceIDLen is the length of a hex trace id (16 bytes).
const traceIDLen = 32

// validTraceID reports whether id is exactly 32 lowercase hex characters, the
// form generateTraceID and the W3C trace ids have.
func validTraceID(id string) bool {
	if len(id) != traceIDLen {
		return false
	}

	for i := range len(id) {
		if c := id[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}
