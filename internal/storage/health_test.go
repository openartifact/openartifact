// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/openartifact/openartifact/internal/metrics"
)

// probeStore is a blob store that only answers HealthCheck.
type probeStore struct {
	BlobStore

	err    error
	probes atomic.Int32
	// block makes a probe wait for its context, to observe the deadline.
	block  bool
	gotCtx atomic.Pointer[context.Context]
}

func (s *probeStore) HealthCheck(ctx context.Context) error {
	s.probes.Add(1)
	s.gotCtx.Store(&ctx)

	if s.block {
		<-ctx.Done()

		return ctx.Err()
	}

	return s.err
}

func TestCheckHealth(t *testing.T) {
	if err := CheckHealth(context.Background(), &probeStore{}); err != nil {
		t.Errorf("healthy store: %v", err)
	}

	want := errors.New("down")
	if err := CheckHealth(context.Background(), &probeStore{err: want}); !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}

	// Nothing to probe is healthy.
	var plain BlobStore = struct{ BlobStore }{}
	if err := CheckHealth(context.Background(), plain); err != nil {
		t.Errorf("a store without a probe: %v", err)
	}
}

func TestMetricsBlobStoreForwardsHealthCheck(t *testing.T) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	want := errors.New("bucket unreachable")
	if err := NewMetricsBlobStore(&probeStore{err: want}, reg).HealthCheck(context.Background()); !errors.Is(err, want) {
		t.Errorf("the wrapped store's error is lost: %v", err)
	}

	dir := t.TempDir()

	fs, err := NewFilesystemStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	m := NewMetricsBlobStore(fs, reg)
	if err := m.HealthCheck(context.Background()); err != nil {
		t.Fatalf("healthy filesystem: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := m.HealthCheck(context.Background()); err == nil {
		t.Error("a missing blob directory passed the check through the wrapper")
	}
}

func TestHealthProbeCachesForTheTTL(t *testing.T) {
	store := &probeStore{}
	p := NewHealthProbe(store, 10*time.Second, time.Second)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	for range 5 {
		if err := p.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if got := store.probes.Load(); got != 1 {
		t.Fatalf("probes within the ttl = %d, want 1", got)
	}

	// A failure shows up only once the cached result is stale, and is cached too.
	store.err = errors.New("down")
	now = now.Add(9 * time.Second)

	if err := p.Check(context.Background()); err != nil {
		t.Errorf("a fresh result was re-probed: %v", err)
	}

	now = now.Add(2 * time.Second)

	for range 3 {
		if err := p.Check(context.Background()); !errors.Is(err, store.err) {
			t.Errorf("got %v, want the failure", err)
		}
	}

	if got := store.probes.Load(); got != 2 {
		t.Errorf("probes = %d, want 2", got)
	}
}

func TestHealthProbeIsBoundedAndSurvivesAHangup(t *testing.T) {
	store := &probeStore{block: true}
	p := NewHealthProbe(store, time.Minute, 50*time.Millisecond)

	// The caller's context is already cancelled: the probe still runs, with its
	// own deadline, and what it learns is its own, not the hangup.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()

	err := p.Check(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the probe deadline", err)
	}

	if time.Since(start) > 5*time.Second {
		t.Error("the probe is not bounded")
	}

	got := store.gotCtx.Load()
	if got == nil {
		t.Fatal("no probe ran")
	}

	if _, ok := (*got).Deadline(); !ok {
		t.Error("the probe has no deadline")
	}
}

func TestHealthProbeNilAndPlainStoreAreHealthy(t *testing.T) {
	var p *HealthProbe
	if err := p.Check(context.Background()); err != nil {
		t.Errorf("nil probe: %v", err)
	}

	if err := NewHealthProbe(struct{ BlobStore }{}, time.Second, time.Second).Check(context.Background()); err != nil {
		t.Errorf("plain store: %v", err)
	}
}
