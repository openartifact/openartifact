// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/httpx"
	"github.com/openartifact/openartifact/internal/metrics"
)

func MetricsMiddleware(reg *metrics.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := httpx.WrapStatus(w)

			// The middleware wraps the router from outside, where chi has not
			// created its route context yet. Seed one: chi reuses it and fills
			// in the matched pattern, which is read back below.
			if chi.RouteContext(r.Context()) == nil {
				r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
			}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)

			pattern := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				pattern = rctx.RoutePattern()
			}

			path := PathLabel(pattern, r)
			statusStr := statusLabel(wrapped.Status())
			reg.HTTPRequestsTotal.WithLabelValues(methodLabel(r.Method), path, statusStr).Inc()
			reg.HTTPRequestDurationSeconds.WithLabelValues(methodLabel(r.Method), path).Observe(duration.Seconds())

			if format, ok := uploadFormat(r, pattern); ok {
				reg.CountUpload(format, metrics.UploadResult(wrapped.Status()))
			}
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

// statusLabels holds the label of every valid status code, so the per-request
// label is a table lookup and not an allocation.
//
//nolint:gochecknoglobals // immutable lookup table
var statusLabels = func() [600]string {
	var t [600]string

	for i := range t {
		t[i] = strconv.Itoa(i)
	}

	return t
}()

func statusLabel(code int) string {
	if code >= 0 && code < len(statusLabels) {
		return statusLabels[code]
	}

	return strconv.Itoa(code)
}
