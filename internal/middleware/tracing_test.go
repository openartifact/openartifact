// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestTracingMiddlewareEchoesOnlyHexTraceIDs(t *testing.T) {
	const valid = "0123456789abcdef0123456789abcdef"

	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)

	tests := []struct {
		name, in string
		echoed   bool
	}{
		{"valid", valid, true},
		{"empty", "", false},
		{"uppercase", "0123456789ABCDEF0123456789ABCDEF", false},
		{"short", "abc123", false},
		{"long", valid + "0", false},
		{"non hex", "0123456789abcdef0123456789abcdeg", false},
		{"header injection chars", "0123456789abcdef0123456789abc<>\"", false},
		{"free text", "<script>alert(1)</script>", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var inCtx any

			h := TracingMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				inCtx = r.Context().Value(TraceIDKey)
			}))

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil)
			req.Header["X-Trace-Id"] = []string{tc.in}

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			got := rr.Header().Get("X-Trace-Id")
			if !hex32.MatchString(got) {
				t.Fatalf("X-Trace-Id %q is not 32 lowercase hex characters", got)
			}

			if (got == tc.in) != tc.echoed {
				t.Errorf("X-Trace-Id %q for client value %q: echoed = %v, want %v", got, tc.in, got == tc.in, tc.echoed)
			}

			if inCtx != got {
				t.Errorf("context trace id %v, header %q", inCtx, got)
			}
		})
	}
}
