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

	if err := probeHealth(addr, time.Second); err != nil {
		t.Fatalf("200 should be healthy: %v", err)
	}

	status = http.StatusServiceUnavailable
	if err := probeHealth(addr, time.Second); err == nil {
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

	if err := probeHealth(addr, time.Second); err == nil {
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
