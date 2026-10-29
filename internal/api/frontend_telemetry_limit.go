// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// userLimiterSweepSize is the number of tracked users above which idle
	// buckets are dropped; userLimiterSweepEvery spaces the sweeps out.
	userLimiterSweepSize  = 4096
	userLimiterSweepEvery = time.Minute
)

// userLimiter is a token bucket per user id: perSecond tokens refill every
// second and burst can be spent at once.
type userLimiter struct {
	mu        sync.Mutex
	perSecond float64
	burst     int
	buckets   map[string]*rate.Limiter
	lastSweep time.Time
	now       func() time.Time
}

func newUserLimiter(perSecond float64, burst int) *userLimiter {
	return &userLimiter{perSecond: perSecond, burst: burst, buckets: map[string]*rate.Limiter{}, now: time.Now}
}

// allow takes one token for user.
func (l *userLimiter) allow(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	b := l.buckets[user]
	if b == nil {
		l.sweep(now)

		b = rate.NewLimiter(rate.Limit(l.perSecond), l.burst)
		l.buckets[user] = b
	}

	return b.AllowN(now, 1)
}

// sweep drops the buckets that have refilled completely (idle users) once the
// table is large, so the number of distinct users cannot grow it forever.
func (l *userLimiter) sweep(now time.Time) {
	if len(l.buckets) < userLimiterSweepSize || now.Sub(l.lastSweep) < userLimiterSweepEvery {
		return
	}

	l.lastSweep = now

	for k, b := range l.buckets {
		if b.TokensAt(now) >= float64(l.burst) {
			delete(l.buckets, k)
		}
	}
}
