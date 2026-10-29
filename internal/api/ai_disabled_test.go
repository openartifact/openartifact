// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build noai

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestAIRoutesAnswerNotImplemented(t *testing.T) {
	r := chi.NewRouter()
	RegisterAIUnavailable(r, "/api/v1/repositories/{name}")

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/repositories/r/ai-analysis"},
		{http.MethodGet, "/api/v1/repositories/r/ai-analysis/a1"},
		{http.MethodPost, "/api/v1/repositories/r/ai-analysis/a1/trigger"},
		{http.MethodGet, "/api/v1/admin/ai/config"},
		{http.MethodPut, "/api/v1/admin/ai/config"},
		{http.MethodPost, "/api/v1/admin/ai/test"},
		{http.MethodGet, "/api/v1/admin/ai/models"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rw := httptest.NewRecorder()
			r.ServeHTTP(rw, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader("{}")))

			if rw.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501", rw.Code)
			}

			var body struct{ Code, Error string }
			if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}

			if body.Code != "feature_unavailable" || !strings.Contains(body.Error, "-tags noai") {
				t.Errorf("body = %+v", body)
			}
		})
	}
}
