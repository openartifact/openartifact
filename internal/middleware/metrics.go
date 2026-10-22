// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/metrics"
)

func MetricsMiddleware(reg *metrics.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)

			path := "unknown"

			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if p := rctx.RoutePattern(); p != "" {
					path = strings.TrimSuffix(p, "/*")
				}
			}

			statusStr := strconv.Itoa(wrapped.statusCode)
			reg.HTTPRequestsTotal.WithLabelValues(methodLabel(r.Method), path, statusStr).Inc()
			reg.HTTPRequestDurationSeconds.WithLabelValues(methodLabel(r.Method), path).Observe(duration.Seconds())
		})
	}
}

// methodLabel bounds the method label cardinality: net/http accepts any RFC
// token as a method, so unknown methods collapse into "OTHER".
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}

	return "OTHER"
}
