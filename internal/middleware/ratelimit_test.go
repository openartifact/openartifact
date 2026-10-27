// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func rlRequest(h http.Handler, peer, xff string) int {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = peer

	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec.Code
}

func rlHandler(t *testing.T, limit int, window time.Duration, trusted *TrustedProxies) http.Handler {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return RateLimitMiddleware(ctx, limit, window, trusted)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func TestRateLimitPerIPBuckets(t *testing.T) {
	t.Parallel()

	h := rlHandler(t, 2, time.Hour, nil)

	for i := range 2 {
		if got := rlRequest(h, "198.51.100.1:1000", ""); got != http.StatusNoContent {
			t.Fatalf("request %d from first IP = %d, want 204", i, got)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "198.51.100.1:2000" // new source port, same IP

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", rec.Code)
	}

	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("429 Content-Type = %q", ct)
	}

	if got := rlRequest(h, "198.51.100.2:1000", ""); got != http.StatusNoContent {
		t.Fatalf("other IP = %d, want its own bucket (204)", got)
	}
}

func TestRateLimitForwardedFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trusted *TrustedProxies
		peer    string
		xffs    []string
		want    []int
	}{
		{
			name:    "untrusted peer cannot split buckets with spoofed XFF",
			trusted: NewTrustedProxies([]string{"10.0.0.0/8"}),
			peer:    "203.0.113.9:4000",
			xffs:    []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"},
			want:    []int{204, 204, 429},
		},
		{
			name:    "no trusted proxies configured ignores XFF",
			trusted: nil,
			peer:    "203.0.113.9:4000",
			xffs:    []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"},
			want:    []int{204, 204, 429},
		},
		{
			name:    "trusted proxy gets one bucket per forwarded client",
			trusted: NewTrustedProxies([]string{"10.0.0.0/8"}),
			peer:    "10.1.2.3:4000",
			xffs:    []string{"1.1.1.1", "1.1.1.1", "1.1.1.1", "2.2.2.2"},
			want:    []int{204, 204, 429, 204},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := rlHandler(t, 2, time.Hour, tt.trusted)

			for i, xff := range tt.xffs {
				if got := rlRequest(h, tt.peer, xff); got != tt.want[i] {
					t.Fatalf("request %d (xff %s) = %d, want %d", i, xff, got, tt.want[i])
				}
			}
		})
	}
}

func TestRateLimitRefill(t *testing.T) {
	t.Parallel()

	// 1 token per 100ms.
	h := rlHandler(t, 1, 100*time.Millisecond, nil)

	const peer = "198.51.100.7:1"

	if got := rlRequest(h, peer, ""); got != http.StatusNoContent {
		t.Fatalf("first = %d", got)
	}

	deadline := time.Now().Add(5 * time.Second)

	for {
		if rlRequest(h, peer, "") == http.StatusNoContent {
			return
		}

		if time.Now().After(deadline) {
			t.Fatal("bucket never refilled")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestRateLimitJanitorStopsOnCancel(t *testing.T) {
	// Not parallel: counts goroutines of the whole process.
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	_ = RateLimitMiddleware(ctx, 1, time.Hour, nil)

	if runtime.NumGoroutine() <= before {
		t.Skip("janitor goroutine not observable yet")
	}

	cancel()

	deadline := time.Now().Add(5 * time.Second)

	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("janitor goroutine leaked: %d > %d", runtime.NumGoroutine(), before)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
