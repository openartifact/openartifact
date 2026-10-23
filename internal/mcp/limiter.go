// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// limiterSweepSize is the number of tracked callers above which idle
	// entries are dropped; limiterSweepEvery spaces the sweeps out.
	limiterSweepSize  = 4096
	limiterSweepEvery = time.Minute
)

// limiter is a per-key token bucket: perMinute tokens refill evenly over a
// minute and a full minute's worth can be spent in a burst. A nil limiter
// allows everything.
type limiter struct {
	mu        sync.Mutex
	perMinute int
	buckets   map[string]*rate.Limiter
	lastSweep time.Time
	now       func() time.Time
}

// newLimiter returns nil (unlimited) for perMinute <= 0.
func newLimiter(perMinute int) *limiter {
	if perMinute <= 0 {
		return nil
	}

	return &limiter{perMinute: perMinute, buckets: map[string]*rate.Limiter{}, now: time.Now}
}

// allow takes one token for key.
func (l *limiter) allow(key string) bool {
	if l == nil {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	b := l.buckets[key]
	if b == nil {
		l.sweep(now)

		b = rate.NewLimiter(rate.Limit(float64(l.perMinute)/60), l.perMinute) //nolint:mnd // per minute to per second
		l.buckets[key] = b
	}

	return b.AllowN(now, 1)
}

// retryAfter is how long a rejected caller should wait for one token.
func (l *limiter) retryAfter() time.Duration {
	if l == nil {
		return 0
	}

	return time.Minute / time.Duration(l.perMinute)
}

// sweep drops buckets that have refilled completely (idle callers) once the
// table is large, so unbounded distinct callers cannot grow it forever.
func (l *limiter) sweep(now time.Time) {
	if len(l.buckets) < limiterSweepSize || now.Sub(l.lastSweep) < limiterSweepEvery {
		return
	}

	l.lastSweep = now

	for k, b := range l.buckets {
		if b.TokensAt(now) >= float64(l.perMinute) {
			delete(l.buckets, k)
		}
	}
}
