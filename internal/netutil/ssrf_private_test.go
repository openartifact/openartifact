// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestValidateOutboundURLContextWith(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "false")

	tests := []struct {
		name    string
		url     string
		private bool
		wantErr bool
	}{
		{"public address", "https://8.8.8.8", false, false},
		{"private address refused by default", "https://10.96.0.1", false, true},
		{"private address admitted per call", "https://10.96.0.1", true, false},
		{"loopback address admitted per call", "https://127.0.0.1:6443", true, false},
		{"cloud metadata stays blocked", "https://169.254.169.254", true, true},
		{"link-local stays blocked", "https://169.254.1.1", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOutboundURLContextWith(context.Background(), tt.url, tt.private)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateOutboundURLContextWith(%q, %v) = %v, wantErr %v", tt.url, tt.private, err, tt.wantErr)
			}
		})
	}
}

func TestSafeHTTPClientWithOptionsAllowPrivate(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "false")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(srv.Close)

	tests := []struct {
		name    string
		opts    ClientOptions
		wantErr bool
	}{
		{"loopback is blocked by default", ClientOptions{}, true},
		{"loopback is reachable when allowed", ClientOptions{AllowPrivate: true}, false},
		{"connections are not kept alive on request", ClientOptions{AllowPrivate: true, DisableKeepAlives: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, http.NoBody)
			if err != nil {
				t.Fatal(err)
			}

			resp, err := SafeHTTPClientWithOptions(5*time.Second, tt.opts).Do(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Do = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil {
				_ = resp.Body.Close()
			}
		})
	}
}
