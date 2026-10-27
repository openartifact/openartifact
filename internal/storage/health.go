// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"sync"
	"time"
)

// HealthChecker is implemented by stores that can probe their backend. Every
// store that wraps another one (metrics, tiers, replication, presign)
// implements it by forwarding to the store it wraps, so the probe reaches the
// backend from whatever layer the caller holds.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// CheckHealth probes s. A store with nothing to probe is healthy.
func CheckHealth(ctx context.Context, s BlobStore) error {
	if hc, ok := s.(HealthChecker); ok {
		return hc.HealthCheck(ctx) //nolint:wrapcheck // transparent wrapper
	}

	return nil
}

// HealthProbe is a time-limited, cached storage probe, so an unauthenticated
// endpoint cannot turn requests into backend round trips: the result of a
// probe is reused for ttl, and a probe is bounded by timeout whatever the
// caller's context does. Concurrent callers share one probe.
type HealthProbe struct {
	store   BlobStore
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time

	mu        sync.Mutex
	checkedAt time.Time
	err       error
}

// NewHealthProbe returns a probe of store.
func NewHealthProbe(store BlobStore, ttl, timeout time.Duration) *HealthProbe {
	return &HealthProbe{store: store, ttl: ttl, timeout: timeout, now: time.Now}
}

// Check returns the outcome of the last probe while it is younger than the
// ttl, else probes again. A nil probe, or one over a store with nothing to
// probe, is healthy. The error is the backend's: callers log it and must not
// put it in a response.
func (p *HealthProbe) Check(ctx context.Context) error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.checkedAt.IsZero() && p.now().Sub(p.checkedAt) < p.ttl {
		return p.err
	}

	// Detached from the caller: a client that hangs up must not leave its
	// cancellation cached as a storage failure for the next ttl.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.timeout)
	defer cancel()

	p.err = CheckHealth(ctx, p.store)
	p.checkedAt = p.now()

	return p.err
}
