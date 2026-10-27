// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package passwd is the local-password policy and Argon2id hashing. It has no
// dependency on the rest of the server so the break-glass resetpw binary can
// use it without linking the database, telemetry and storage stacks.
package passwd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 1
	argonMemory  = 65536
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// argonSemaphore caps concurrent Argon2id operations to prevent OOM under load.
//
//nolint:gochecknoglobals // rate-limits concurrent Argon2id hashing
var argonSemaphore = make(chan struct{}, runtime.NumCPU())

const (
	// MinPasswordLength is the minimum length of any local password, enforced
	// on every path that sets one (self-service change, admin create/patch,
	// resetpw).
	MinPasswordLength = 12
	// MaxPasswordLength bounds the input handed to Argon2id.
	MaxPasswordLength = 1024
)

var (
	// ErrPasswordTooShort is returned by ValidatePassword for short passwords.
	ErrPasswordTooShort = errors.New("password must be at least " + strconv.Itoa(MinPasswordLength) + " characters")
	// ErrPasswordTooLong is returned by ValidatePassword for oversized passwords.
	ErrPasswordTooLong = errors.New("password must be at most " + strconv.Itoa(MaxPasswordLength) + " characters")
)

// ValidatePassword is the single password policy shared by every code path that
// sets a local password.
func ValidatePassword(plain string) error {
	switch n := utf8.RuneCountInString(plain); {
	case n < MinPasswordLength:
		return ErrPasswordTooShort
	case len(plain) > MaxPasswordLength:
		return ErrPasswordTooLong
	}

	return nil
}

//nolint:gochecknoglobals // lazily computed decoy hash for constant-time login failures
var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// BurnPasswordVerification runs a full Argon2id verification against a decoy
// hash. Call it on login paths where no real hash exists (unknown user,
// disabled account, SSO-only account) so that the response time does not reveal
// which usernames exist.
func BurnPasswordVerification(plain string) {
	dummyHashOnce.Do(func() {
		dummyHash, _ = HashPassword("openartifact-decoy-password")
	})

	if dummyHash != "" {
		_, _ = VerifyPassword(plain, dummyHash)
	}
}

// HashPassword hashes a plaintext password using Argon2id and returns an encoded string.
// Format: $argon2id$v=19$m=65536,t=1,p=4$<base64-no-padding-salt>$<base64-no-padding-hash>.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	argonSemaphore <- struct{}{}

	hash := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	<-argonSemaphore

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads, b64Salt, b64Hash), nil
}

// VerifyPassword checks a plaintext password against an encoded Argon2id hash.
// Returns (true, nil) on match, (false, nil) on mismatch, (false, err) on malformed input.
func VerifyPassword(plain, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// Expected: ["", "argon2id", "v=19", "m=65536,t=1,p=4", "<salt>", "<hash>"]
	if len(parts) != 6 {
		return false, fmt.Errorf(
			"invalid encoded hash: expected 6 segments, got %d",
			len(parts),
		) //nolint:err113 // dynamic error with runtime context
	}

	if parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid algorithm: %s", parts[1]) //nolint:err113 // dynamic error with runtime context
	}

	var (
		memory, time uint32
		threads      uint8
	)
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("parse params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}

	argonSemaphore <- struct{}{}

	derivedHash := argon2.IDKey(
		[]byte(plain),
		salt,
		time,
		memory,
		threads,
		uint32(len(expectedHash)),
	) //nolint:gosec // argon2 requires uint32 for key length

	<-argonSemaphore

	if subtle.ConstantTimeCompare(derivedHash, expectedHash) != 1 {
		return false, nil
	}

	return true, nil
}
