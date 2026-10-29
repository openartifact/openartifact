// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package passwd

import (
	"encoding/base64"
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

func TestVerifyPassword_RejectsOutOfBoundsParams(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	shortSalt := base64.RawStdEncoding.EncodeToString(make([]byte, 2))
	hugeKey := base64.RawStdEncoding.EncodeToString(make([]byte, 4096))

	tests := []struct {
		name    string
		params  string
		salt    string
		key     string
		wantErr bool
	}{
		{"default parameters", "m=65536,t=1,p=4", salt, key, false},
		{"zero passes (argon2 panics)", "m=65536,t=0,p=4", salt, key, true},
		{"zero lanes (argon2 panics)", "m=65536,t=1,p=0", salt, key, true},
		{"too many lanes", "m=65536,t=1,p=255", salt, key, true},
		{"too many passes", "m=65536,t=4000000000,p=4", salt, key, true},
		{"4 TiB of memory", "m=4294967295,t=1,p=4", salt, key, true},
		{"memory above the cap", "m=1048576,t=1,p=4", salt, key, true},
		{"memory below 8 per lane", "m=8,t=1,p=4", salt, key, true},
		{"negative lanes", "m=65536,t=1,p=-1", salt, key, true},
		{"short salt", "m=65536,t=1,p=4", shortSalt, key, true},
		{"huge hash", "m=65536,t=1,p=4", salt, hugeKey, true},
		{"empty hash", "m=65536,t=1,p=4", salt, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := "$argon2id$v=19$" + tt.params + "$" + tt.salt + "$" + tt.key

			ok, err := VerifyPassword("some password", encoded)
			if ok {
				t.Fatal("a bogus hash verified")
			}

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestVerifyPassword_MalformedHashesDoNotLeakSemaphoreSlots: after far more
// rejected hashes than the semaphore has slots, hashing and verifying still work.
func TestVerifyPassword_MalformedHashesDoNotLeakSemaphoreSlots(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))

	for _, params := range []string{"m=65536,t=0,p=4", "m=65536,t=1,p=0", "m=4294967295,t=1,p=4", "m=x"} {
		for range 4 * cap(argonSemaphore) {
			if _, err := VerifyPassword("pw", "$argon2id$v=19$"+params+"$"+salt+"$"+key); err == nil {
				t.Fatalf("%s: expected an error", params)
			}
		}
	}

	if n := len(argonSemaphore); n != 0 {
		t.Fatalf("%d semaphore slots still held", n)
	}

	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := VerifyPassword("correct horse battery", hash); err != nil || !ok {
		t.Fatalf("verify after bad hashes = %v, %v", ok, err)
	}
}
