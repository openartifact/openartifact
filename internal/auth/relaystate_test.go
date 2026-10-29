// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestSignVerifyRelayState_roundtrip(t *testing.T) {
	key := "test-secret-key"
	redirect := "https://example.com/dashboard"

	encoded := SignRelayState(redirect, key)

	got, err := VerifyRelayState(encoded, key)
	if err != nil {
		t.Fatalf("VerifyRelayState returned error: %v", err)
	}

	if got != redirect {
		t.Fatalf("expected %q, got %q", redirect, got)
	}
}

func TestVerifyRelayState_tamper(t *testing.T) {
	key := "test-secret-key"
	redirect := "https://example.com/dashboard"

	encoded := SignRelayState(redirect, key)

	_, err := VerifyRelayState(encoded+"X", key)
	if !errors.Is(err, ErrInvalidRelayState) {
		t.Fatalf("expected ErrInvalidRelayState, got %v", err)
	}
}

func TestVerifyRelayState_wrongKey(t *testing.T) {
	redirect := "https://example.com/page"
	encoded := SignRelayState(redirect, "key-a")

	_, err := VerifyRelayState(encoded, "key-b")
	if !errors.Is(err, ErrInvalidRelayState) {
		t.Fatalf("expected ErrInvalidRelayState, got %v", err)
	}
}

func TestVerifyRelayState_invalidBase64(t *testing.T) {
	_, err := VerifyRelayState("!!!not-base64!!!", "key")
	if !errors.Is(err, ErrInvalidRelayState) {
		t.Fatalf("expected ErrInvalidRelayState, got %v", err)
	}
}

func TestVerifyRelayState_age(t *testing.T) {
	signed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	encoded := signRelayStateAt("/ui/repos?a=b|c", "key", signed)

	tests := []struct {
		name string
		now  time.Time
		ok   bool
	}{
		{"same second", signed, true},
		{"just under the max age", signed.Add(relayStateMaxAge), true},
		{"past the max age", signed.Add(relayStateMaxAge + time.Second), false},
		{"days later", signed.Add(72 * time.Hour), false},
		{"clock a little behind", signed.Add(-relayStateSkew), true},
		{"issued in the future", signed.Add(-relayStateSkew - time.Second), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := verifyRelayStateAt(encoded, "key", tc.now)
			if tc.ok {
				if err != nil || got != "/ui/repos?a=b|c" {
					t.Fatalf("verify = %q, %v; want the redirect", got, err)
				}

				return
			}

			if !errors.Is(err, ErrInvalidRelayState) {
				t.Fatalf("verify err = %v, want ErrInvalidRelayState", err)
			}
		})
	}
}

// TestVerifyRelayState_formats refuses states that were not made by
// SignRelayState: the old "redirect|mac" form (no age, no domain prefix), a
// MAC without the domain prefix, and a state whose timestamp was changed.
func TestVerifyRelayState_formats(t *testing.T) {
	now := time.Now()
	issued := strconv.FormatInt(now.Unix(), 10)
	bareMAC := func(msg string) string {
		m := hmac.New(sha256.New, []byte("key"))
		m.Write([]byte(msg))

		return hex.EncodeToString(m.Sum(nil))
	}
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	tests := []struct {
		name    string
		encoded string
	}{
		{"legacy redirect|mac", enc("/ui|" + bareMAC("/ui"))},
		{"mac without the domain prefix", enc(issued + "|/ui|" + bareMAC(issued+"\x00/ui"))},
		{"mac over the redirect only", enc(issued + "|/ui|" + bareMAC("/ui"))},
		{"timestamp rewritten", enc(strconv.FormatInt(now.Unix()+1, 10) + "|/ui|" + relayStateMAC(issued, "/ui", "key"))},
		{"no timestamp", enc("/ui|" + relayStateMAC("", "/ui", "key"))},
		{"non-numeric timestamp", enc("x|/ui|" + relayStateMAC("x", "/ui", "key"))},
		{"empty", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := verifyRelayStateAt(tc.encoded, "key", now); !errors.Is(err, ErrInvalidRelayState) {
				t.Fatalf("verify err = %v, want ErrInvalidRelayState", err)
			}
		})
	}
}
