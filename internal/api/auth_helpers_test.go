// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A body over the cap is an error, not a prefix to decode: the padding after a
// complete value used to be cut off silently, and a long value was cut mid-way.
// A body of exactly the cap still decodes.
func TestDecodeJSONRejectsOversizeBodies(t *testing.T) {
	padded := func(n int) string { return `{"name":"a"}` + strings.Repeat(" ", n-len(`{"name":"a"}`)) }

	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"at the cap", padded(int(maxRequestBodyBytes)), nil},
		{"one byte over", padded(int(maxRequestBodyBytes) + 1), errBodyTooLarge},
		{"far over", padded(2 * int(maxRequestBodyBytes)), errBodyTooLarge},
		{"long value", `{"name":"` + strings.Repeat("a", int(maxRequestBodyBytes)) + `"}`, errBodyTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tt.body))

			var dst map[string]any

			if err := decodeJSON(req, &dst); !errors.Is(err, tt.wantErr) {
				t.Fatalf("decodeJSON(%d bytes) = %v, want %v", len(tt.body), err, tt.wantErr)
			}
		})
	}
}

// writeBadJSON answers 413 for an oversize body (however it was capped) and
// 400 for any other rejection, and never repeats the error text.
func TestWriteBadJSON(t *testing.T) {
	capped := &http.MaxBytesError{Limit: 5}

	for _, tt := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"too large", errBodyTooLarge, http.StatusRequestEntityTooLarge, "request_entity_too_large"},
		{"capped by the handler", capped, http.StatusRequestEntityTooLarge, "request_entity_too_large"},
		{"nul", errNULInBody, http.StatusBadRequest, "bad_request"},
		{"syntax", errors.New("invalid character 'x' looking for beginning of value"), http.StatusBadRequest, "bad_request"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeBadJSON(rec, tt.err)

			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.body) {
				t.Fatalf("writeBadJSON(%v) = %d %s, want %d with %q", tt.err, rec.Code, rec.Body.String(), tt.code, tt.body)
			}

			if strings.Contains(rec.Body.String(), tt.err.Error()) {
				t.Errorf("response repeats the error text: %s", rec.Body.String())
			}
		})
	}
}

// A U+0000 escape in a JSON body is rejected (handlers answer 400); an escaped
// backslash followed by the text u0000 is ordinary data.
func TestDecodeJSONRejectsNULEscapes(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantNUL bool
	}{
		{"plain", `{"name":"a"}`, false},
		{"nul escape", `{"name":"a\u0000b"}`, true},
		{"nul escape in key", `{"a\u0000":"b"}`, true},
		{"escaped backslash then text", `{"name":"a\\u0000b"}`, false},
		{"escaped backslash then nul escape", `{"name":"a\\\u0000b"}`, true},
		{"other control escape", `{"name":"a\u0001b"}`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tt.body))

			var dst map[string]any

			err := decodeJSON(req, &dst)
			if got := errors.Is(err, errNULInBody); got != tt.wantNUL {
				t.Fatalf("decodeJSON(%s) NUL error = %v (err %v), want %v", tt.body, got, err, tt.wantNUL)
			}

			if !tt.wantNUL && err != nil {
				t.Fatalf("decodeJSON(%s) = %v, want nil", tt.body, err)
			}
		})
	}
}
