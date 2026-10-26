// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"regexp"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/passwd"
)

var argon2idTestPattern = regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=1,p=4\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$`)

func TestPasswordHash_Format(t *testing.T) {
	hash, err := passwd.HashPassword("hunter2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !argon2idTestPattern.MatchString(hash) {
		t.Errorf("hash format mismatch: %s", hash)
	}
}

func TestPasswordVerify_Correct(t *testing.T) {
	hash, err := passwd.HashPassword("hunter2")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	ok, err := passwd.VerifyPassword("hunter2", hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if !ok {
		t.Error("expected true for correct password")
	}
}

func TestPasswordVerify_Wrong(t *testing.T) {
	hash, err := passwd.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	ok, err := passwd.VerifyPassword("wrong-password", hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ok {
		t.Error("expected false for wrong password")
	}
}

func TestPasswordHash_DifferentSalts(t *testing.T) {
	h1, err := passwd.HashPassword("same-password")
	if err != nil {
		t.Fatalf("hash1: %v", err)
	}

	h2, err := passwd.HashPassword("same-password")
	if err != nil {
		t.Fatalf("hash2: %v", err)
	}

	if h1 == h2 {
		t.Error("expected two hashes of same password to differ due to random salt")
	}
}

func TestPasswordHash_Concurrent(t *testing.T) {
	const n = 8

	plaintexts := []string{
		"password1", "password2", "password3", "password4",
		"password5", "password6", "password7", "password8",
	}

	results := make([]string, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	wg.Add(n)

	for i := range n {
		go func() {
			defer wg.Done()

			h, err := passwd.HashPassword(plaintexts[i])
			results[i] = h
			errs[i] = err
		}()
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d error: %v", i, err)
		}
	}

	seen := make(map[string]bool, n)

	for i, h := range results {
		if h == "" {
			t.Errorf("goroutine %d produced empty hash", i)
		}

		if seen[h] {
			t.Errorf("goroutine %d produced duplicate hash", i)
		}

		seen[h] = true
	}
}
