// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package crypto

import (
	"errors"
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
