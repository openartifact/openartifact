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
