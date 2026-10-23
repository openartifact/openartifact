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
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The metrics middleware wraps the router from outside; the path label must
// still be the route template, not "unknown".
func TestMetricsMiddlewarePathLabel(t *testing.T) {
	reg := metrics.NewRegistry()

	r := chi.NewRouter()
	r.Get("/api/v1/things/{id}", func(http.ResponseWriter, *http.Request) {})

	h := MetricsMiddleware(reg)(r)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/things/42", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := testutil.ToFloat64(reg.HTTPRequestsTotal.WithLabelValues("GET", "/api/v1/things/{id}", "200")); got != 1 {
		t.Errorf("route-template series = %v, want 1", got)
	}

	if got := testutil.ToFloat64(reg.HTTPRequestsTotal.WithLabelValues("GET", "unknown", "200")); got != 0 {
		t.Errorf("unknown series = %v, want 0", got)
	}

	// No matching route: a fixed label, never the raw path.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/no/such/route/secret-token", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := testutil.ToFloat64(reg.HTTPRequestsTotal.WithLabelValues("GET", "unmatched", "404")); got != 1 {
		t.Errorf("unmatched series = %v, want 1", got)
	}
}
