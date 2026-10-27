// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
)

func TestSystemInfo_AllKeysPresent(t *testing.T) {
	handler := api.NewSystemInfoHandler("v1.0.0", "abc1234", "2026-01-01T00:00:00Z", nil, nil, "")
	router := chi.NewRouter()
	handler.RegisterAuthed(router)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/admin/system/info", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	required := []string{"version", "commit", "build_time", "go_version", "os", "arch"}
	for _, key := range required {
		if _, ok := body[key]; !ok {
			t.Errorf("missing key %q in response", key)
		}
	}

	if body["go_version"] != runtime.Version() {
		t.Errorf("go_version=%q, want %q", body["go_version"], runtime.Version())
	}

	if body["version"] != "v1.0.0" {
		t.Errorf("version=%q, want v1.0.0", body["version"])
	}
}

// The store the server hands to the handler is the metrics wrapper: its health
// must reach the backend, and a failure must show as degraded without any
// detail in the unauthenticated response.
func TestSystemInfo_ReportsStorageHealthThroughTheMetricsWrapper(t *testing.T) {
	const secretDetail = "dial tcp 10.0.0.7:9000: secret-internal-detail"

	for _, tc := range []struct {
		name       string
		err        error
		wantStatus string
		wantPublic string
	}{
		{"healthy", nil, "ok", "ok"},
		{"unreachable", errors.New(secretDetail), "error", "degraded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMetricsBlobStore(&healthStore{err: tc.err}, metrics.NewRegistryOn(prometheus.NewRegistry()))
			router := chi.NewRouter()
			handler := api.NewSystemInfoHandler("v1.0.0", "abc1234", "2026-01-01T00:00:00Z", nil, store, "s3")
			handler.RegisterPublic(router)
			handler.RegisterAuthed(router)

			authed := getJSONBody(t, router, "/api/v1/admin/system/info")
			if authed["storage_status"] != tc.wantStatus {
				t.Errorf("storage_status = %v, want %s", authed["storage_status"], tc.wantStatus)
			}

			public := getJSONBody(t, router, "/api/v1/system/info")
			if public["status"] != tc.wantPublic {
				t.Errorf("public status = %v, want %s", public["status"], tc.wantPublic)
			}

			raw, _ := json.Marshal([]any{authed, public})
			if strings.Contains(string(raw), "secret-internal-detail") {
				t.Errorf("a storage error detail leaked: %s", raw)
			}
		})
	}
}

func getJSONBody(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))

	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}

	return decodeJSONMap(t, w)
}
