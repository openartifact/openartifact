// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stalledServer answers only after the client gave up (or after a few seconds,
// so a missing timeout fails the test instead of hanging it).
func stalledServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestNewClientBoundsEveryCallByDefault(t *testing.T) {
	for name, hc := range map[string]*http.Client{
		"default http client":       nil,
		"caller client w/o timeout": {},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewClient("http://example.invalid", "tok", hc)
			if c.timeout != defaultRequestTimeout {
				t.Fatalf("timeout = %v, want the default %v", c.timeout, defaultRequestTimeout)
			}
		})
	}
}

func TestClientDoGivesUpOnAStalledServer(t *testing.T) {
	srv := stalledServer(t)
	c := NewClient(srv.URL, "tok", srv.Client())
	c.timeout = 100 * time.Millisecond

	start := time.Now()
	err := c.Get(t.Context(), "/api/v1/anything", nil)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline exceeded", err)
	}

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("call took %v, want it cut at the client timeout", took)
	}
}

func TestClientDoKeepsAnEarlierCallerDeadline(t *testing.T) {
	srv := stalledServer(t)
	c := NewClient(srv.URL, "tok", srv.Client())

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.Get(ctx, "/api/v1/anything", nil)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline exceeded", err)
	}

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("call took %v, want the caller's 100ms deadline to apply, not the %v default", took, defaultRequestTimeout)
	}
}
