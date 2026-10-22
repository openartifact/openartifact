// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxBodyMiddleware(t *testing.T) {
	var readErr error

	h := MaxBodyMiddleware(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))

	// Declared length over the cap: rejected before the handler runs.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", strings.NewReader(strings.Repeat("a", 11)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize: got %d, want 413", rec.Code)
	}

	// Unknown length (chunked): the reader fails once the cap is crossed.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", io.NopCloser(strings.NewReader(strings.Repeat("a", 11))))
	req.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), req)

	if _, ok := errors.AsType[*http.MaxBytesError](readErr); !ok {
		t.Fatalf("chunked oversize: read error = %v, want *http.MaxBytesError", readErr)
	}

	// Within the cap passes; limit 0 disables.
	readErr = nil
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", strings.NewReader("0123456789"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil {
		t.Fatalf("at-cap body failed: %v", readErr)
	}

	readErr = nil
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", strings.NewReader(strings.Repeat("a", 100)))
	rec = httptest.NewRecorder()
	MaxBodyMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.Copy(io.Discard, r.Body)
	})).ServeHTTP(rec, req)

	if readErr != nil {
		t.Fatalf("disabled cap failed: %v", readErr)
	}
}
