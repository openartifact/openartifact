// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/config"
)

func TestProbeHealth(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.WriteHeader(status)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()

	if err := probeHealth("http", addr, time.Second); err != nil {
		t.Fatalf("200 should be healthy: %v", err)
	}

	status = http.StatusServiceUnavailable
	if err := probeHealth("http", addr, time.Second); err == nil {
		t.Fatal("503 should be unhealthy")
	}
}

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

func TestProbeHealthConnectionRefused(t *testing.T) {
	addr := closedAddr(t)

	if err := probeHealth("http", addr, time.Second); err == nil {
		t.Fatal("closed port should be unhealthy")
	}
}

func TestHealthcheckFlagUsesConfiguredPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SERVER_PORT", port)
	t.Setenv("OPENARTIFACT_CONFIG", "")

	if err := run([]string{"-healthcheck"}); err != nil {
		t.Fatalf("healthcheck against live server: %v", err)
	}

	_, dead, _ := net.SplitHostPort(closedAddr(t))

	t.Setenv("SERVER_PORT", dead)

	if err := run([]string{"-healthcheck"}); err == nil {
		t.Fatal("expected failure when nothing listens")
	}
}

func TestProbeHealthTLSLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()

	if err := probeHealth("https", addr, 2*time.Second); err != nil {
		t.Fatalf("https loopback self-probe must skip verification: %v", err)
	}

	if err := probeHealth("http", addr, 2*time.Second); err == nil {
		t.Fatal("plain http against a TLS listener must fail")
	}
}

func TestHealthTarget(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		tls        bool
		wantScheme string
		wantAddr   string
	}{
		{"wildcard v4", "0.0.0.0", false, "http", "127.0.0.1:8080"},
		{"wildcard v6", "::", false, "http", "127.0.0.1:8080"},
		{"empty", "", false, "http", "127.0.0.1:8080"},
		{"concrete v4", "10.1.2.3", true, "https", "10.1.2.3:8080"},
		{"concrete v6", "fd00::5", false, "http", "[fd00::5]:8080"},
		{"bracketed wildcard", "[::]", true, "https", "127.0.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, addr := healthTarget(config.ServerConfig{Host: tt.host, Port: 8080, TLSEnabled: tt.tls})
			if scheme != tt.wantScheme || addr != tt.wantAddr {
				t.Fatalf("got %s %s, want %s %s", scheme, addr, tt.wantScheme, tt.wantAddr)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "localhost": true, "10.0.0.1": false, "example.com": false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q)=%v, want %v", host, got, want)
		}
	}
}
