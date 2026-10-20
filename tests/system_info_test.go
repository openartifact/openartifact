// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
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
