// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
)

const wantChallenge = `Bearer realm="openartifact", resource_metadata="https://artifacts.example.com/.well-known/oauth-protected-resource/mcp"`

func listBody(t *testing.T) string {
	t.Helper()

	return rpcBody(t, 1, "tools/list", nil)
}

func TestMCPRequiresBearerCredential(t *testing.T) {
	st := newStack(t, nil)

	cookie := func() *http.Cookie {
		value, sess, err := auth.CreateSession(t.Context(), st.db, "reader", "", "")
		if err != nil {
			t.Fatal(err)
		}

		st.db.sessions[sess.TokenHash] = sess

		return &http.Cookie{Name: auth.SessionCookieName, Value: value}
	}()

	tests := []struct {
		name    string
		user    string
		headers map[string]string
		cookie  *http.Cookie
		want    int
	}{
		{"valid bearer token", "reader", nil, nil, http.StatusOK},
		{"no credential", "", nil, nil, http.StatusUnauthorized},
		{"unknown token", "", map[string]string{"Authorization": "Bearer oa_pat_" + strings.Repeat("0", 64)}, nil, http.StatusUnauthorized},
		{"garbage bearer", "", map[string]string{"Authorization": "Bearer not-a-token"}, nil, http.StatusUnauthorized},
		{"valid token as basic password", "", map[string]string{"Authorization": "Basic " + basic("reader", st.secrets["reader"])}, nil, http.StatusUnauthorized},
		{"valid session cookie", "", nil, cookie, http.StatusUnauthorized},
		{"cookie next to a valid token still authenticates by the token only", "reader", nil, cookie, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(listBody(t)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Protocol-Version", "2025-06-18")

			if tt.user != "" {
				req.Header.Set("Authorization", "Bearer "+st.secrets[tt.user])
			}

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}

			rec := httptest.NewRecorder()
			st.handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}

			if tt.want == http.StatusUnauthorized && !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), wantChallenge) {
				t.Errorf("WWW-Authenticate = %q, want it to start with %q", rec.Header().Get("WWW-Authenticate"), wantChallenge)
			}
		})
	}
}

func TestMCPChallengeNamesInvalidToken(t *testing.T) {
	st := newStack(t, nil)

	anon := st.do(http.MethodPost, "", listBody(t), nil)
	if strings.Contains(anon.Header().Get("WWW-Authenticate"), "error=") {
		t.Errorf("a request without credentials must not carry an error code: %q", anon.Header().Get("WWW-Authenticate"))
	}

	bad := st.do(http.MethodPost, "", listBody(t), map[string]string{"Authorization": "Bearer nope"})
	if !strings.Contains(bad.Header().Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("WWW-Authenticate = %q, want error=\"invalid_token\"", bad.Header().Get("WWW-Authenticate"))
	}
}

func TestMCPWithoutBaseURLChallengeOmitsMetadata(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.baseURL = "" })

	rec := st.do(http.MethodPost, "", listBody(t), nil)

	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("status %d, challenge %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestMCPRefusesGetAndDelete(t *testing.T) {
	st := newStack(t, nil)

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if rec := st.do(method, "reader", "", nil); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
	}
}

func TestMCPOriginValidation(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.origins = []string{"https://agents.example.org"} })

	tests := []struct {
		name   string
		origin string
		user   string
		want   int
	}{
		{"no origin header", "", "reader", http.StatusOK},
		{"base url origin", "https://artifacts.example.com", "reader", http.StatusOK},
		{"base url origin, other case", "HTTPS://Artifacts.Example.COM", "reader", http.StatusOK},
		{"configured origin", "https://agents.example.org", "reader", http.StatusOK},
		{"foreign origin", "https://evil.example.net", "reader", http.StatusForbidden},
		{"same host, other scheme", "http://artifacts.example.com", "reader", http.StatusForbidden},
		{"same host, other port", "https://artifacts.example.com:8443", "reader", http.StatusForbidden},
		{"null origin", "null", "reader", http.StatusForbidden},
		{"foreign origin is refused before authentication", "https://evil.example.net", "", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := st.do(http.MethodPost, tt.user, listBody(t), map[string]string{"Origin": tt.origin})
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestMCPOriginAllowlistEmptyWithoutBaseURL(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.baseURL = "" })

	if rec := st.do(http.MethodPost, "reader", listBody(t), map[string]string{"Origin": "https://artifacts.example.com"}); rec.Code != http.StatusForbidden {
		t.Fatalf("with no BASE_URL and no configured origins every Origin must be refused, got %d", rec.Code)
	}
}

func TestMCPBodyLimit(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.server.MaxBodyBytes = 300 })

	big := rpcBody(t, 1, "tools/call", map[string]any{"name": "get_repository", "arguments": map[string]any{"name": strings.Repeat("a", 400)}})

	t.Run("declared length over the cap", func(t *testing.T) {
		if rec := st.do(http.MethodPost, "reader", big, nil); rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("streamed body over the cap", func(t *testing.T) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(big))
		req.ContentLength = -1 // chunked: no declared length to reject early
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		req.Header.Set("Authorization", "Bearer "+st.secrets["reader"])

		rec := httptest.NewRecorder()
		st.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("small request still works", func(t *testing.T) {
		if rec := st.do(http.MethodPost, "reader", listBody(t), nil); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestMCPDefaultBodyLimitIsOneMiB(t *testing.T) {
	st := newStack(t, nil)

	if st.mcp.maxBodyBytes != 1<<20 {
		t.Fatalf("default cap = %d, want 1 MiB", st.mcp.maxBodyBytes)
	}
}

func TestMCPBatchRefusedWhereBatchingWasRemoved(t *testing.T) {
	st := newStack(t, nil)
	batch := "[" + listBody(t) + "]"

	tests := []struct {
		version string
		want    int
	}{
		{"2025-06-18", http.StatusBadRequest},
		{"2025-11-25", http.StatusBadRequest},
		{"2026-07-28", http.StatusBadRequest},
		{"2025-03-26", http.StatusOK}, // batching still exists in that revision
		{"", http.StatusOK},           // no header means 2025-03-26
	}

	for _, tt := range tests {
		t.Run("version "+tt.version, func(t *testing.T) {
			rec := st.do(http.MethodPost, "reader", batch, map[string]string{"MCP-Protocol-Version": tt.version})
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}

			if tt.want != http.StatusBadRequest {
				return
			}

			var resp toolResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error == nil || resp.Error.Code != jsonRPCInvalidRequest {
				t.Fatalf("want JSON-RPC error -32600, got %s", rec.Body.String())
			}
		})
	}
}

func TestMCPRateLimitPerPrincipal(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.server.RateLimitPerMinute = 3 })

	now := time.Now()
	st.mcp.requestLimiter.now = func() time.Time { return now }

	for i := range 3 {
		if rec := st.do(http.MethodPost, "reader", listBody(t), nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i+1, rec.Code)
		}
	}

	limited := st.do(http.MethodPost, "reader", listBody(t), nil)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("4th request: status %d, Retry-After %q", limited.Code, limited.Header().Get("Retry-After"))
	}

	if rec := st.do(http.MethodPost, "admin", listBody(t), nil); rec.Code != http.StatusOK {
		t.Errorf("another principal must have its own budget, got %d", rec.Code)
	}

	now = now.Add(time.Minute)

	if rec := st.do(http.MethodPost, "reader", listBody(t), nil); rec.Code != http.StatusOK {
		t.Errorf("budget must refill over time, got %d", rec.Code)
	}
}
