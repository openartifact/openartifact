// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
)

// benchWriter is a response writer that discards the body, so the benchmark
// measures the middleware and not the recorder.
type benchWriter struct {
	header http.Header
	status int
}

func (w *benchWriter) Header() http.Header { return w.header }
func (w *benchWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *benchWriter) Write(p []byte) (int, error) { return len(p), nil }

// serverChain mounts the base middleware in the order cmd/server installs it,
// in front of a router with one JSON route and one artifact route.
func serverChain(b *testing.B) http.Handler {
	b.Helper()

	log, err := logger.New("info", "json", "/dev/null")
	if err != nil {
		b.Fatal(err)
	}

	router := chi.NewRouter()
	for _, mw := range []func(http.Handler) http.Handler{
		MaxBodyMiddleware(1 << 30),
		SecurityHeadersMiddleware(false),
		RecoveryMiddleware(log),
		RequestIDMiddleware,
		MetricsMiddleware(metrics.NewRegistryOn(prometheus.NewRegistry())),
		telemetry.HTTPMiddleware(),
		TracingMiddleware,
		LoggingMiddleware(log),
		CORSMiddleware(nil),
		RejectNULBytes,
	} {
		router.Use(mw)
	}

	body := []byte(`{"ok":true}`)
	router.Get("/api/v1/auth/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	router.Get("/repos/raw/{name}/*", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	})

	return router
}

// BenchmarkMiddlewareChain is the fixed cost every request pays before its
// handler runs: the whole base chain around a handler that writes 11 bytes.
func BenchmarkMiddlewareChain(b *testing.B) {
	h := serverChain(b)

	for _, tc := range []struct{ name, path string }{
		{"api", "/api/v1/auth/me"},
		{"artifact", "/repos/raw/r1/file.bin"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, tc.path, http.NoBody)
			w := &benchWriter{header: http.Header{}}

			b.ReportAllocs()

			for b.Loop() {
				clear(w.header)

				w.status = 0
				h.ServeHTTP(w, req)
			}
		})
	}
}
