// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	if err := Init("0123456789abcdef-test-key"); err != nil {
		t.Fatal(err)
	}
	defer Init("")
	c, err := Encrypt("s3cret")
	if err != nil || c == "s3cret" || !IsEncrypted(c) {
		t.Fatalf("bad ciphertext %q %v", c, err)
	}
	p, err := Decrypt(c)
	if err != nil || p != "s3cret" {
		t.Fatalf("got %q %v", p, err)
	}
	if p, _ := Decrypt("legacy"); p != "legacy" {
		t.Fatal("legacy plaintext must pass through")
	}
	if _, err := Decrypt(c[:len(c)-4]); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestNoKey(t *testing.T) {
	Init("")
	if c, _ := Encrypt("x"); c != "x" {
		t.Fatal("no key must pass through")
	}
	if _, err := Decrypt("enc:v1:AAAA"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey got %v", err)
	}
	if err := Init("short"); err == nil {
		t.Fatal("short key must fail")
	}
}

func TestEncryptAlwaysSeals(t *testing.T) {
	if err := Init("0123456789abcdef-test-key"); err != nil {
		t.Fatal(err)
	}
	defer Init("")
	first, err := Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encrypt(first)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("Encrypt must not pass enc: input through")
	}
	p, err := Decrypt(second)
	if err != nil || p != first {
		t.Fatalf("double-sealed value must decrypt to the original ciphertext, got %q %v", p, err)
	}
}

func TestCheckPlaintext(t *testing.T) {
	if err := CheckPlaintext("hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := CheckPlaintext(""); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"enc:v1:AAAA", "enc:v2:AAAA"} {
		if err := CheckPlaintext(in); !errors.Is(err, ErrEncryptedInput) {
			t.Fatalf("%s: want ErrEncryptedInput got %v", in, err)
		}
	}
}

const testKey = "0123456789abcdef-test-key"

func TestEncryptWritesV2(t *testing.T) {
	if err := Init(testKey); err != nil {
		t.Fatal(err)
	}
	defer Init("")
	c, err := Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c, "enc:v2:") {
		t.Fatalf("new ciphertext must use enc:v2:, got %q", c)
	}
	if p, err := Decrypt(c); err != nil || p != "s3cret" {
		t.Fatalf("v2 round trip: got %q %v", p, err)
	}
}

func TestDecryptLegacyV1(t *testing.T) {
	// Sealed by the pre-HKDF implementation (key = SHA-256(testKey), nonce "fixednonce12").
	const legacy = "enc:v1:Zml4ZWRub25jZTEysjMZoJwlSoAZFySzAvKwcyVH93G0TBB+XbyH4PM="
	if !IsEncrypted(legacy) {
		t.Fatal("enc:v1: values must still be recognised as encrypted")
	}
	if err := Init(testKey); err != nil {
		t.Fatal(err)
	}
	defer Init("")
	p, err := Decrypt(legacy)
	if err != nil || p != "legacy-secret" {
		t.Fatalf("legacy v1 ciphertext must decrypt, got %q %v", p, err)
	}
	if err := Init(testKey + "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(legacy); err == nil {
		t.Fatal("v1 ciphertext must not open under a different key")
	}
}

func TestV2KeyDiffersFromV1(t *testing.T) {
	if err := Init(testKey); err != nil {
		t.Fatal(err)
	}
	defer Init("")
	c, err := Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	// Relabelled as v1, the value is opened with the SHA-256 key and must fail.
	if _, err := Decrypt("enc:v1:" + strings.TrimPrefix(c, "enc:v2:")); err == nil {
		t.Fatal("v2 ciphertext must not open with the v1 (SHA-256) key")
	}
}

func TestV2TamperAndWrongKey(t *testing.T) {
	if err := Init(testKey); err != nil {
		t.Fatal(err)
	}
	defer Init("")
	c, err := Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(c, "enc:v2:"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01
	if _, err := Decrypt("enc:v2:" + base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered v2 ciphertext must fail")
	}
	if _, err := Decrypt("enc:v2:AAAA"); err == nil {
		t.Fatal("truncated v2 ciphertext must fail")
	}
	if err := Init(testKey + "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(c); err == nil {
		t.Fatal("v2 ciphertext must not open under a different key")
	}
	Init("")
	if _, err := Decrypt(c); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey got %v", err)
	}
}
