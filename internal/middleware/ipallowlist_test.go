// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPAllowlistNeverFailsOpen(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	code := func(entries []string, remote string) int {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		IPAllowlistMiddleware(entries, nil)(ok).ServeHTTP(w, r)

		return w.Code
	}

	if c := code([]string{"not-an-ip"}, "198.51.100.1:1"); c != http.StatusForbidden {
		t.Errorf("unparsable-only list: got %d, want 403", c)
	}

	if c := code([]string{"203.0.113.5"}, "203.0.113.5:1"); c != http.StatusNoContent {
		t.Errorf("bare IP entry must allow itself, got %d", c)
	}

	if c := code([]string{"203.0.113.5"}, "198.51.100.1:1"); c != http.StatusForbidden {
		t.Errorf("bare IP entry must exclude others, got %d", c)
	}

	if c := code(nil, "198.51.100.1:1"); c != http.StatusNoContent {
		t.Errorf("empty list allows all, got %d", c)
	}
}
