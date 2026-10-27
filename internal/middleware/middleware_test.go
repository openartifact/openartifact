// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/pkg/logger"
)

func TestRequestIDMiddleware(t *testing.T) {
	t.Parallel()

	var seen []string

	h := RequestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Request-ID"))
	}))

	var got []string

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		// A client-supplied ID must not be trusted.
		req.Header.Set("X-Request-ID", "client-chosen")

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got = append(got, rec.Header().Get("X-Request-ID"))
	}

	for i, id := range got {
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("response id %q is not a UUID: %v", id, err)
		}

		if seen[i] != id {
			t.Errorf("handler saw %q, response carries %q", seen[i], id)
		}
	}

	if got[0] == got[1] {
		t.Error("two requests share a request id")
	}
}

func TestRecoveryMiddlewarePassesThrough(t *testing.T) {
	t.Parallel()

	log, err := logger.New("error", "json", filepath.Join(t.TempDir(), "log.json"))
	if err != nil {
		t.Fatal(err)
	}

	h := RecoveryMiddleware(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
}
