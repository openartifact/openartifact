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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPathLabel(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		url     string
		want    string
	}{
		{"unmatched", "", "/whatever/secret", "unmatched"},
		{"template", "/api/v1/repositories/{name}", "/api/v1/repositories/acme", "/api/v1/repositories/{name}"},
		{"wildcard stays a template", "/repos/raw/{name}/*", "/repos/raw/acme/a/b/c.bin", "/repos/raw/{name}/*"},
		{"spa catch-all is not empty", "/*", "/dashboard", "/*"},
		{"oci manifest", "/v2/*", "/v2/acme/org/app/manifests/latest", "/v2/{name}/manifests/{reference}"},
		{"oci manifest digest", "/v2/*", "/v2/acme/app/manifests/sha256:abc", "/v2/{name}/manifests/{reference}"},
		{"oci blob", "/v2/*", "/v2/acme/app/blobs/sha256:abc", "/v2/{name}/blobs/{digest}"},
		{"oci upload start", "/v2/*", "/v2/acme/app/blobs/uploads/", "/v2/{name}/blobs/uploads"},
		{"oci upload start no slash", "/v2/*", "/v2/acme/app/blobs/uploads", "/v2/{name}/blobs/uploads"},
		{"oci upload", "/v2/*", "/v2/acme/app/blobs/uploads/2f1b", "/v2/{name}/blobs/uploads/{uuid}"},
		{"oci tags", "/v2/*", "/v2/acme/app/tags/list", "/v2/{name}/tags/list"},
		{"oci referrers", "/v2/*", "/v2/acme/app/referrers/sha256:abc", "/v2/{name}/referrers/{digest}"},
		{"oci unknown", "/v2/*", "/v2/acme/app/other", "/v2/*"},
		{"oci bare name", "/v2/*", "/v2/acme", "/v2/*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.url, nil)

			if got := PathLabel(tt.pattern, r); got != tt.want {
				t.Errorf("PathLabel(%q, %q) = %q, want %q", tt.pattern, tt.url, got, tt.want)
			}
		})
	}
}

func TestUploadFormatOCI(t *testing.T) {
	tests := []struct {
		method, url string
		want        bool
	}{
		{http.MethodPut, "/v2/acme/app/manifests/latest", true},
		{http.MethodGet, "/v2/acme/app/manifests/latest", false},
		{http.MethodHead, "/v2/acme/app/manifests/latest", false},
		{http.MethodDelete, "/v2/acme/app/manifests/latest", false},
		{http.MethodPut, "/v2/acme/app/blobs/uploads/abc?digest=sha256:1", true},
		{http.MethodPatch, "/v2/acme/app/blobs/uploads/abc", false},
		{http.MethodPost, "/v2/acme/app/blobs/uploads/", false},
		{http.MethodPost, "/v2/acme/app/blobs/uploads/?mount=sha256:1&from=x", false},
		{http.MethodPost, "/v2/acme/app/blobs/uploads/?digest=sha256:1", true},
		{http.MethodGet, "/v2/acme/app/blobs/sha256:1", false},
		{http.MethodGet, "/v2/acme/app/tags/list", false},
	}

	for _, tt := range tests {
		r := httptest.NewRequestWithContext(t.Context(), tt.method, tt.url, nil)

		format, got := uploadFormat(r, ociWildcard)
		if got != tt.want {
			t.Errorf("%s %s counted = %v, want %v", tt.method, tt.url, got, tt.want)
		}

		if got && format != metrics.FormatDocker {
			t.Errorf("%s %s format = %q, want docker", tt.method, tt.url, format)
		}
	}
}

func TestCountUploadsWrapper(t *testing.T) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	status := http.StatusCreated
	h := CountUploads(reg, metrics.FormatGo, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })

	call := func() {
		h(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", nil))
	}

	call()

	status = http.StatusInternalServerError

	call()
	call()

	if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("go", "success")); got != 1 {
		t.Errorf("go success = %v, want 1", got)
	}

	if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("go", "error")); got != 2 {
		t.Errorf("go error = %v, want 2", got)
	}
}

// A handler that never calls WriteHeader answers 200: a success.
func TestCountUploadsImplicitOK(t *testing.T) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())
	h := CountUploads(reg, metrics.FormatGo, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	h(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", nil))

	if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("go", "success")); got != 1 {
		t.Errorf("go success = %v, want 1", got)
	}
}

// End to end through chi: a counted route increments uploads_total with the
// route's format, and the upload is not counted twice nor for other methods.
func TestMetricsMiddlewareCountsUploads(t *testing.T) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())
	status := http.StatusCreated

	r := chi.NewRouter()
	stub := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
	r.Put("/repos/raw/{name}/*", stub)
	r.Get("/repos/raw/{name}/*", stub)
	r.Put("/repos/maven/{name}/*", stub) // counted by its handler, not here

	h := MetricsMiddleware(reg)(r)
	do := func(method, url string) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), method, url, nil))
	}

	do(http.MethodPut, "/repos/raw/acme/a/b.bin")
	do(http.MethodGet, "/repos/raw/acme/a/b.bin")
	do(http.MethodPut, "/repos/maven/acme/g/a/1/a-1.jar")

	status = http.StatusConflict

	do(http.MethodPut, "/repos/raw/acme/a/b.bin")

	if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("raw", "success")); got != 1 {
		t.Errorf("raw success = %v, want 1", got)
	}

	if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("raw", "error")); got != 1 {
		t.Errorf("raw error = %v, want 1", got)
	}

	if n := testutil.CollectAndCount(reg.UploadsTotal); n != 2 {
		t.Errorf("upload series = %d, want 2 (maven is counted by its handler)", n)
	}
}
