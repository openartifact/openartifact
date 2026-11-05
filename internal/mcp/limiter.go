// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// limiterMaxBuckets caps the number of tracked callers: at the cap idle
	// entries are dropped (at most once per limiterSweepEvery) and, if none
	// is idle, the fullest bucket is evicted.
	limiterMaxBuckets = 4096
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

// sweep makes room for one more bucket once the table is at its cap: it drops
// buckets that have refilled completely (idle callers) and, when that frees
// nothing, evicts the fullest bucket (the least recently active caller), so
// the table never exceeds limiterMaxBuckets.
func (l *limiter) sweep(now time.Time) {
	if len(l.buckets) < limiterMaxBuckets {
		return
	}

	dropIdle := now.Sub(l.lastSweep) >= limiterSweepEvery
	if dropIdle {
		l.lastSweep = now
	}

	var (
		fullest   string
		fullestAt = -1.0
	)

	for k, b := range l.buckets {
		tokens := b.TokensAt(now)
		if dropIdle && tokens >= float64(l.perMinute) {
			delete(l.buckets, k)

			continue
		}

		if tokens > fullestAt {
			fullest, fullestAt = k, tokens
		}
	}

	if len(l.buckets) >= limiterMaxBuckets {
		delete(l.buckets, fullest)
	}
}
