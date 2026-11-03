// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"slices"
	"time"

	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// RequestIDMiddleware adds a unique request ID to each request.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newUUID()
		setRequestID(r.Header, requestID)
		setRequestID(w.Header(), requestID)
		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware logs HTTP requests.
func LoggingMiddleware(log *logger.Logger) func(http.Handler) http.Handler {
	// The access log is the hottest log site: typed fields on the desugared
	// logger skip the sugar's per-call field boxing, and the entry is only
	// built when the level is enabled.
	zl := log.Desugar()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			requestID := requestIDOf(r.Header)

			// The status-recording wrapper is shared with the metrics, tracing
			// and audit layers.
			wrapped := httpx.WrapStatus(w)

			next.ServeHTTP(wrapped, r)

			ce := zl.Check(zapcore.InfoLevel, "HTTP request")
			if ce == nil {
				return
			}

			duration := time.Since(start)
			ce.Write(append([]zap.Field{
				zap.String("request_id", requestID),
				zap.String("method", r.Method),
				// Path only: the query string can carry credentials and OAuth
				// codes/state (SSO callbacks, ?token= style clients).
				zap.String("path", r.URL.Path),
				zap.Int("status", wrapped.Status()),
				zap.Int64("duration_ms", duration.Milliseconds()),
				zap.String("remote_addr", r.RemoteAddr),
				// trace_id/span_id join the line to its OpenTelemetry trace;
				// absent while telemetry is disabled.
			}, telemetry.LogFieldsZap(r.Context())...)...)
		})
	}
}

// RecoveryMiddleware recovers from panics.
func RecoveryMiddleware(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					requestID := requestIDOf(r.Header)
					log.Errorw("panic recovered",
						"request_id", requestID,
						"error", err,
						"path", r.URL.Path,
					)
					httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// CORSMiddleware handles CORS headers.
//
// The browser refuses to deliver a credentialed response when the server
// returns Access-Control-Allow-Origin: * — credentials require an exact
// origin echo plus Access-Control-Allow-Credentials: true. The SPA always
// uses fetch with credentials: 'include' (for the session cookie), so for an
// explicitly listed origin we echo it and advertise credentials.
//
// A "*" entry must never be combined with credentials: reflecting an
// arbitrary Origin together with Allow-Credentials would let any website read
// authenticated responses with the visitor's session cookie. With a wildcard
// the middleware answers a literal "*" without credentials (token-header
// clients still work; cookie-session browsers cross-origin do not), unless the
// origin is also listed explicitly.
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	const (
		allowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
		// X-Requested-With and X-Csrf-Token are sent by the SPA's api client
		// (CSRF double-submit). Without them the preflight rejects every real
		// state-changing request.
		allowedHeaders = "Content-Type, Authorization, X-Requested-With, X-API-Key, X-Csrf-Token"
	)

	wildcard := slices.Contains(allowedOrigins, "*")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Vary tells caches the response depends on the Origin header.
			w.Header().Add("Vary", "Origin")

			switch {
			case origin != "" && slices.Contains(allowedOrigins, origin):
				// Echo the exact, explicitly allowed origin so credentialed
				// responses are accepted.
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
				w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			case origin != "" && wildcard:
				// Public, credential-less access only.
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
				w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
