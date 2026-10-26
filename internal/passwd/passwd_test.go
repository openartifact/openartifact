// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package passwd

import (
	"regexp"
	"testing"
)

var argon2idPattern = regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=1,p=4\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$`)

func TestHashPassword_Format(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !argon2idPattern.MatchString(hash) {
		t.Errorf("hash format mismatch: %s", hash)
	}
}

func TestVerifyPassword_Correct(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	ok, err := VerifyPassword("hunter2", hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if !ok {
		t.Error("expected true for correct password")
	}
}

func TestVerifyPassword_Wrong(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	ok, err := VerifyPassword("wrong", hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ok {
		t.Error("expected false for wrong password")
	}
}

func TestHashPassword_Idempotency(t *testing.T) {
	h1, err := HashPassword("same")
	if err != nil {
		t.Fatalf("hash1: %v", err)
	}

	h2, err := HashPassword("same")
	if err != nil {
		t.Fatalf("hash2: %v", err)
	}

	if h1 == h2 {
		t.Error("expected two hashes of same password to differ (different random salt)")
	}
}

func TestVerifyPassword_Malformed(t *testing.T) {
	_, err := VerifyPassword("pass", "notvalidformat")
	if err == nil {
		t.Error("expected error for malformed encoded string")
	}
}
