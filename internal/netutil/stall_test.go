// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func getBody(t *testing.T, client *http.Client, url string) ([]byte, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	return io.ReadAll(resp.Body)
}

func TestStallTimeoutFailsStalledBody(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // the upstream goes silent mid-body
	}))
	defer srv.Close()

	const stall = 200 * time.Millisecond

	client := SafeHTTPClientWithStallTimeout(time.Minute, 0, stall)
	start := time.Now()

	got, err := getBody(t, client, srv.URL)
	if !errors.Is(err, ErrBodyStalled) {
		t.Fatalf("read error = %v, want ErrBodyStalled (got %q)", err, got)
	}

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("stalled body took %v to fail, stall timeout is %v", elapsed, stall)
	}
}

func TestStallTimeoutIsResetByProgress(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 8 {
			_, _ = io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
	}))
	defer srv.Close()

	// The whole body takes longer than the stall timeout, but no single wait does.
	client := SafeHTTPClientWithStallTimeout(time.Minute, 0, 250*time.Millisecond)

	got, err := getBody(t, client, srv.URL)
	if err != nil || len(got) != 8 {
		t.Fatalf("steady body: %q, %v", got, err)
	}
}

func TestStallTimeoutIgnoresSlowConsumer(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "abcdef")
	}))
	defer srv.Close()

	client := SafeHTTPClientWithStallTimeout(time.Minute, 0, 100*time.Millisecond)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	time.Sleep(300 * time.Millisecond) // consumer is busy, not the upstream

	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != "abcdef" {
		t.Fatalf("read after pause: %q, %v", got, err)
	}
}

// The transport health-checks HTTP/2 connections: an idle one is pinged and a
// connection that does not answer is closed.
func TestTransportPingsIdleHTTP2Connections(t *testing.T) {
	for _, stall := range []time.Duration{0, time.Minute} {
		c := SafeHTTPClientWithStallTimeout(time.Minute, 0, stall)

		var tr *http.Transport

		switch rt := c.Transport.(type) {
		case *http.Transport:
			tr = rt
		case *stallTransport:
			tr, _ = rt.base.(*http.Transport)
		}

		if tr == nil || !tr.ForceAttemptHTTP2 || tr.HTTP2 == nil {
			t.Fatalf("stall=%v: transport %T lacks HTTP/2 config", stall, c.Transport)
		}

		if tr.HTTP2.SendPingTimeout != HTTP2ReadIdleTimeout || tr.HTTP2.PingTimeout != HTTP2PingTimeout {
			t.Fatalf("stall=%v: ping config = %+v", stall, *tr.HTTP2)
		}
	}
}
