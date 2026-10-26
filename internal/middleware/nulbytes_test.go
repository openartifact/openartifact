// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRejectNULBytes(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   int
	}{
		{"plain request", "/api/v1/search?q=widget", http.StatusNoContent},
		{"escaped percent is not a NUL", "/api/v1/search?q=%2500", http.StatusNoContent},
		{"NUL in query", "/api/v1/search?q=a%00b", http.StatusBadRequest},
		{"NUL in an unrelated query key", "/api/v1/bundles?x%00=1", http.StatusBadRequest},
		{"NUL in path", "/api/v1/bundles/a%00b", http.StatusBadRequest},
	}

	h := RejectNULBytes(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, nil))

			if rec.Code != tc.want {
				t.Fatalf("GET %s = %d, want %d", tc.target, rec.Code, tc.want)
			}
		})
	}
}
