// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// b64 is the standard base64 of a hash, the SRI spelling's payload.
func b64(h SHA256) string { return base64.StdEncoding.EncodeToString(h[:]) }

// verifyNarInfo checks a "<name>:<base64>" signature over the fingerprint
// against a "<name>:<base64>" public key. It is independent of the code under
// test except for Fingerprint.
func verifyNarInfo(t *testing.T, publicKey, sig string, n *NarInfo) bool {
	t.Helper()

	pkName, pkB64, _ := strings.Cut(publicKey, ":")
	sigName, sigB64, _ := strings.Cut(sig, ":")

	if pkName != sigName {
		return false
	}

	pub, err := base64.StdEncoding.DecodeString(pkB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("bad public key %q", publicKey)
	}

	raw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("bad signature %q", sig)
	}

	fp, err := n.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	return ed25519.Verify(pub, []byte(fp), raw)
}

func testNarInfo(t *testing.T) *NarInfo {
	t.Helper()

	n, err := ParseNarInfo([]byte(narInfoText()))
	if err != nil {
		t.Fatal(err)
	}

	return n
}

func TestSignAndVerify(t *testing.T) {
	key, err := GenerateKey("cache.example.org-1")
	if err != nil {
		t.Fatal(err)
	}

	n := testNarInfo(t)

	sig, err := key.Sign(n)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(sig, "cache.example.org-1:") {
		t.Fatalf("signature %q lacks the key name", sig)
	}

	if !verifyNarInfo(t, key.PublicKey(), sig, n) {
		t.Fatal("signature does not verify")
	}

	n.NarSize++

	if verifyNarInfo(t, key.PublicKey(), sig, n) {
		t.Fatal("signature verifies a modified narinfo")
	}

	other, err := GenerateKey("cache.example.org-1")
	if err != nil {
		t.Fatal(err)
	}

	if verifyNarInfo(t, other.PublicKey(), sig, testNarInfo(t)) {
		t.Fatal("signature verifies under another key")
	}
}

func TestSignRejectsBadNarHash(t *testing.T) {
	key, _ := GenerateKey("k-1")

	n := testNarInfo(t)
	n.NarHash = "nope"

	if _, err := key.Sign(n); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("Sign error = %v, want ErrInvalidHash", err)
	}
}

func TestSecretKeyRoundTrip(t *testing.T) {
	key, err := GenerateKey("k-1")
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseSecretKey(key.String())
	if err != nil {
		t.Fatal(err)
	}

	if parsed.String() != key.String() || parsed.Name() != "k-1" || parsed.PublicKey() != key.PublicKey() {
		t.Fatalf("parsed key differs: %q vs %q", parsed.String(), key.String())
	}

	n := testNarInfo(t)

	sig, err := parsed.Sign(n)
	if err != nil || !verifyNarInfo(t, key.PublicKey(), sig, n) {
		t.Fatalf("parsed key signature does not verify: %v", err)
	}

	if _, err := ParseSecretKey("  " + key.String() + "\n"); err != nil {
		t.Fatalf("surrounding whitespace rejected: %v", err)
	}
}

func TestParseSecretKeyRejects(t *testing.T) {
	key, _ := GenerateKey("k-1")
	_, b64key, _ := strings.Cut(key.String(), ":")

	raw, _ := base64.StdEncoding.DecodeString(b64key)
	badPub := append([]byte(nil), raw...)
	badPub[len(badPub)-1] ^= 1

	tests := []struct{ name, in string }{
		{"empty", ""},
		{"no colon", "k-1"},
		{"no name", ":" + b64key},
		{"bad name", "k/1:" + b64key},
		{"not base64", "k-1:!!!"},
		{"seed only", "k-1:" + base64.StdEncoding.EncodeToString(raw[:32])},
		{"mismatched public half", "k-1:" + base64.StdEncoding.EncodeToString(badPub)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSecretKey(tc.in); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("error = %v, want ErrInvalidKey", err)
			}
		})
	}
}

func TestKeyNames(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"cache.example.org-1", true},
		{"a", true},
		{strings.Repeat("a", maxKeyNameLen), true},
		{strings.Repeat("a", maxKeyNameLen+1), false},
		{"", false},
		{"a:b", false},
		{"a b", false},
		{"a/b", false},
		{"a\n", false},
	}

	for _, tc := range tests {
		if got := ValidKeyName(tc.in); got != tc.want {
			t.Errorf("ValidKeyName(%q) = %v, want %v", tc.in, got, tc.want)
		}

		_, err := GenerateKey(tc.in)
		if (err == nil) != tc.want {
			t.Errorf("GenerateKey(%q) error = %v, want success %v", tc.in, err, tc.want)
		}
	}
}

func TestPublicKeyFormat(t *testing.T) {
	key, _ := GenerateKey("k-1")

	name, enc, ok := strings.Cut(key.PublicKey(), ":")
	if !ok || name != "k-1" {
		t.Fatalf("PublicKey = %q", key.PublicKey())
	}

	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		t.Fatalf("public key payload = %d bytes, %v; want 32", len(raw), err)
	}
}

func TestRenamed(t *testing.T) {
	key, _ := GenerateKey("old-1")

	renamed, err := key.Renamed("new-2")
	if err != nil {
		t.Fatal(err)
	}

	if renamed.Name() != "new-2" || key.Name() != "old-1" {
		t.Fatalf("names = %q, %q", renamed.Name(), key.Name())
	}

	_, oldPub, _ := strings.Cut(key.PublicKey(), ":")
	_, newPub, _ := strings.Cut(renamed.PublicKey(), ":")

	if oldPub != newPub {
		t.Fatal("renaming changed the key pair")
	}

	if _, err := renamed.Renamed("bad name"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("Renamed bad name error = %v, want ErrInvalidKey", err)
	}
}
