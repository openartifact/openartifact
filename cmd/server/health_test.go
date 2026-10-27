// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
)

// The storage entry of /health must see the backend through the same wrapper
// the server serves with, for every backend that can be probed, and must not
// leak the backend's error.
func TestStorageComponentReportsTheBackendBehindTheWrapper(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")

	raw, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	a, logPath := newTestApp(t, nil)
	a.store.blobs = storage.NewMetricsBlobStore(raw, metrics.NewRegistryOn(prometheus.NewRegistry()))
	a.store.health = storage.NewHealthProbe(a.store.blobs, 0, healthProbeTimeout)

	if got := a.storageComponent(context.Background()); got.Status != "ok" {
		t.Fatalf("healthy backend: %+v", got)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	got := a.storageComponent(context.Background())
	if got.Status != "error" || got.Message != "unavailable" {
		t.Fatalf("lost backend: %+v", got)
	}

	if strings.Contains(got.Message, dir) {
		t.Errorf("the response names the backend path: %q", got.Message)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(logged), "storage check failed") {
		t.Errorf("the failure is not logged: %v %q", err, logged)
	}
}

type failingProbe struct {
	storage.BlobStore

	calls int
}

func (f *failingProbe) HealthCheck(context.Context) error {
	f.calls++
	return errors.New("down")
}

// /health is unauthenticated: its storage probe is cached, so requests do not
// turn into backend round trips.
func TestStorageComponentIsCached(t *testing.T) {
	store := &failingProbe{}
	a, _ := newTestApp(t, nil)
	a.store.health = storage.NewHealthProbe(store, healthProbeTTL, healthProbeTimeout)

	for range 20 {
		if got := a.storageComponent(context.Background()); got.Status != "error" {
			t.Fatalf("got %+v", got)
		}
	}

	if store.calls != 1 {
		t.Errorf("backend probed %d times for 20 requests, want 1", store.calls)
	}
}
