// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"sync"
	"time"
)

const (
	// loginTrustTTL is how long a successful login vouches for its
	// (username, IP) pair.
	loginTrustTTL = 30 * 24 * time.Hour
	// loginTrustMaxKeys bounds the memory of the trust set. Only a login with
	// the correct password adds a key, so it cannot be flooded by guessing.
	loginTrustMaxKeys = 10000
)

// loginTrust remembers the (username, IP) pairs that recently logged in with
// the correct password. While a username is under a distributed guessing
// attack, such a pair is not subject to the shared budget of unknown
// addresses, so the account's real users are never locked out by strangers.
type loginTrust struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

func newLoginTrust() *loginTrust {
	return &loginTrust{seen: make(map[string]time.Time), now: time.Now}
}

// known reports whether key logged in successfully within loginTrustTTL.
func (t *loginTrust) known(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	at, ok := t.seen[key]

	return ok && t.now().Sub(at) < loginTrustTTL
}

// remember records a successful login for key.
func (t *loginTrust) remember(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()

	if _, ok := t.seen[key]; !ok && len(t.seen) >= loginTrustMaxKeys {
		t.evictLocked(now)
	}

	t.seen[key] = now
}

// evictLocked drops the expired keys, or the oldest one when none expired.
func (t *loginTrust) evictLocked(now time.Time) {
	var (
		oldestKey string
		oldest    time.Time
	)

	for k, at := range t.seen {
		if now.Sub(at) >= loginTrustTTL {
			delete(t.seen, k)
			continue
		}

		if oldestKey == "" || at.Before(oldest) {
			oldestKey, oldest = k, at
		}
	}

	if len(t.seen) >= loginTrustMaxKeys {
		delete(t.seen, oldestKey)
	}
}
