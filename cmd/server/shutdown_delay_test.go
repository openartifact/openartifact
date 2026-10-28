// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/pkg/logger"
)

// delayHarness is a running HTTPServer with /ready (the real handler) and a
// plain /ping, plus the signal channel awaitShutdown listens on.
type delayHarness struct {
	a    *app
	srv  *api.HTTPServer
	sigs chan os.Signal
	done chan error
	base string
}

func newDelayHarness(t *testing.T, delaySeconds int) *delayHarness {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := ln.Addr().String()
	_ = ln.Close()

	cfg := &config.Config{}
	cfg.Server.ShutdownDelay = delaySeconds
	a := &app{cfg: cfg, log: log}

	srv := api.NewHTTPServer(addr, log)
	srv.GetRouter().Get("/ready", a.handleReady)
	srv.GetRouter().Get("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = srv.Stop(ctx)
	})

	h := &delayHarness{a: a, srv: srv, sigs: make(chan os.Signal, 2), done: make(chan error, 1), base: "http://" + addr}

	go func() { h.done <- a.awaitShutdown(srv, h.sigs) }()

	return h
}

// get returns the response status and whether the server announced it will
// close the connection.
func (h *delayHarness) get(t *testing.T, path string) (int, bool) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.base+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, resp.Close
}

// waitDraining polls until the shutdown delay started, within a deadline. It
// polls the flag, not /ready: before the flag is set the real handler would
// ping the (absent) database.
func (h *delayHarness) waitDraining(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !h.a.draining.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the server never started draining")
		}

		time.Sleep(5 * time.Millisecond)
	}

	if code, _ := h.get(t, "/ready"); code != http.StatusServiceUnavailable {
		t.Fatalf("/ready while draining = %d, want 503", code)
	}
}

func (h *delayHarness) finished() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

func TestShutdownDelay_ReadinessFlipsWhileServing(t *testing.T) {
	h := newDelayHarness(t, 3)

	if code, _ := h.get(t, "/ping"); code != http.StatusOK {
		t.Fatalf("/ping before signal = %d", code)
	}

	h.sigs <- syscall.SIGTERM
	h.waitDraining(t)

	if h.finished() {
		t.Fatal("awaitShutdown returned before the delay elapsed")
	}

	code, closing := h.get(t, "/ping")
	if code != http.StatusOK {
		t.Fatalf("/ping during the delay = %d, want 200", code)
	}

	if !closing {
		t.Error("keep-alives still enabled during the delay")
	}

	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("awaitShutdown error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("awaitShutdown did not return after the delay")
	}
}

func TestShutdownDelay_SecondSignalSkipsDelay(t *testing.T) {
	h := newDelayHarness(t, 3600)

	h.sigs <- syscall.SIGTERM
	h.waitDraining(t)

	if h.finished() {
		t.Fatal("awaitShutdown returned before the second signal")
	}

	h.sigs <- syscall.SIGINT

	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("awaitShutdown error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second signal did not end the delay")
	}
}

func TestShutdownDelay_ZeroSkipsDelay(t *testing.T) {
	h := newDelayHarness(t, 0)

	h.sigs <- syscall.SIGTERM

	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("awaitShutdown error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("awaitShutdown did not return with no delay")
	}

	if h.a.draining.Load() {
		t.Error("draining set although the delay is 0")
	}
}
