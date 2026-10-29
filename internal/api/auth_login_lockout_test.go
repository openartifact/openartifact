// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/passwd"
	"github.com/openartifact/openartifact/pkg/logger"
)

const lockoutPassword = "correct-horse-battery"

type lockoutEnv struct {
	h  *AuthHandler
	db *loginExpiryDB
}

func newLockoutEnv(t *testing.T) *lockoutEnv {
	t.Helper()

	hash, err := passwd.HashPassword(lockoutPassword)
	if err != nil {
		t.Fatal(err)
	}

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		t.Fatal(err)
	}

	db := &loginExpiryDB{user: &models.User{ID: "u1", Username: "admin", PasswordHash: &hash, Kind: models.UserKindHuman}}
	h := NewAuthHandler(db, log, nil, nil)
	// A smaller distributed threshold keeps the test to a few hashes; the
	// behaviour above it is what is under test.
	h.loginAnyIPLimiter = newSlidingWindowLimiter(12, loginUserFailureWindow)

	return &lockoutEnv{h: h, db: db}
}

// login posts a login for admin from ip and returns the recorder.
func (e *lockoutEnv) login(t *testing.T, ip, password string) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://oa.example.com/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
	r.Host = "oa.example.com"
	r.RemoteAddr = ip + ":40000"
	r.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	e.h.handleLogin(w, r)

	return w
}

func TestLoginLockoutCannotBeUsedToLockOutTheOwner(t *testing.T) {
	const usualIP = "203.0.113.7"

	e := newLockoutEnv(t)

	if w := e.login(t, usualIP, lockoutPassword); w.Code != http.StatusOK {
		t.Fatalf("first login = %d: %s", w.Code, w.Body.String())
	}

	// The attacker rotates addresses so no single pair reaches its limit
	// before the username-wide counter does.
	for i := 1; i <= 5; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i)
		for range 5 {
			e.login(t, ip, "wrong-password")
		}
	}

	if _, blocked := e.h.loginAnyIPLimiter.retryAfter(limiterKey("login:", "admin")); !blocked {
		t.Fatal("the username-wide failure counter did not trip, the scenario is not exercised")
	}

	t.Run("owner from the usual address keeps logging in", func(t *testing.T) {
		for i := range loginUnknownIPLimit + 2 {
			if w := e.login(t, usualIP, lockoutPassword); w.Code != http.StatusOK {
				t.Fatalf("login %d from the usual IP = %d: %s", i, w.Code, w.Body.String())
			}
		}
	})

	t.Run("strangers share a small budget", func(t *testing.T) {
		var limited *httptest.ResponseRecorder

		for i := range 2 * loginUnknownIPLimit {
			w := e.login(t, fmt.Sprintf("192.0.2.%d", i+1), "wrong-password")
			if w.Code == http.StatusTooManyRequests {
				limited = w
				break
			}
		}

		if limited == nil {
			t.Fatal("attempts from unknown addresses were never limited")
		}

		if limited.Header().Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	})

	t.Run("a pair that was never limited is not blocked before the password is checked", func(t *testing.T) {
		fresh := newLockoutEnv(t)
		if w := fresh.login(t, "192.0.2.200", lockoutPassword); w.Code != http.StatusOK {
			t.Fatalf("login with no attack in progress = %d", w.Code)
		}
	})
}

func TestLoginBruteForceFromOneAddressIsStillLimited(t *testing.T) {
	e := newLockoutEnv(t)

	const attackerIP = "198.51.100.99"

	for i := range loginUserFailureLimit {
		if w := e.login(t, attackerIP, fmt.Sprintf("guess-%d", i)); w.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d = %d, want 401", i, w.Code)
		}
	}

	// Locked out for the pair: even the right password is refused.
	w := e.login(t, attackerIP, lockoutPassword)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt after %d failures = %d, want 429", loginUserFailureLimit, w.Code)
	}

	if e.db.sessions != 0 {
		t.Errorf("%d sessions created for a locked-out pair", e.db.sessions)
	}

	// Another address is not affected.
	if w := e.login(t, "203.0.113.8", lockoutPassword); w.Code != http.StatusOK {
		t.Fatalf("owner from another address = %d: %s", w.Code, w.Body.String())
	}
}

func TestLoginTrustBoundsItsMemory(t *testing.T) {
	tr := newLoginTrust()

	for i := range loginTrustMaxKeys + 50 {
		tr.remember(fmt.Sprintf("login:u|%d", i))
	}

	if n := len(tr.seen); n > loginTrustMaxKeys {
		t.Fatalf("%d keys tracked, cap is %d", n, loginTrustMaxKeys)
	}

	if !tr.known(fmt.Sprintf("login:u|%d", loginTrustMaxKeys+49)) {
		t.Error("the newest login was not remembered")
	}
}
