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

// Bounds VerifyPassword accepts in a stored hash. They sit well above the
// parameters HashPassword writes and well below what could exhaust the host.
const (
	maxArgonMemory  = 256 * 1024 // KiB (256 MiB)
	maxArgonTime    = 10
	maxArgonThreads = 16
	minArgonSaltLen = 8
	maxArgonSaltLen = 64
	minArgonKeyLen  = 16
	maxArgonKeyLen  = 64
)

// ErrInvalidHashParams is returned by VerifyPassword for an encoded hash whose
// parameters are outside the accepted bounds.
var ErrInvalidHashParams = errors.New("argon2id parameters out of bounds")

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
		return false, fmt.Errorf( //nolint:err113 // dynamic error with runtime context
			"invalid encoded hash: expected 6 segments, got %d",
			len(parts),
		)
	}

	if parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid algorithm: %s", parts[1]) //nolint:err113 // dynamic error with runtime context
	}

	memory, time, threads, err := parseArgonParams(parts[3])
	if err != nil {
		return false, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}

	if len(salt) < minArgonSaltLen || len(salt) > maxArgonSaltLen {
		return false, fmt.Errorf("%w: salt length %d", ErrInvalidHashParams, len(salt))
	}

	if len(expectedHash) < minArgonKeyLen || len(expectedHash) > maxArgonKeyLen {
		return false, fmt.Errorf("%w: hash length %d", ErrInvalidHashParams, len(expectedHash))
	}

	derivedHash := deriveBounded([]byte(plain), salt, time, memory, threads, uint32(len(expectedHash))) //nolint:gosec // bounded above

	if subtle.ConstantTimeCompare(derivedHash, expectedHash) != 1 {
		return false, nil
	}

	return true, nil
}

// deriveBounded runs one Argon2id derivation holding a slot of argonSemaphore;
// the deferred release keeps the slot even if the derivation panics.
func deriveBounded(plain, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	argonSemaphore <- struct{}{}

	defer func() { <-argonSemaphore }()

	return argon2.IDKey(plain, salt, time, memory, threads, keyLen)
}

// parseArgonParams parses the "m=<KiB>,t=<passes>,p=<lanes>" segment of an
// encoded hash and enforces the bounds below. The values come from the stored
// hash, which an attacker with write access to the users table (or a hash
// imported from elsewhere) controls: argon2.IDKey panics for t < 1 or p < 1
// and would allocate m KiB.
func parseArgonParams(segment string) (memory, passes uint32, threads uint8, err error) {
	var lanes uint32

	if _, err = fmt.Sscanf(segment, "m=%d,t=%d,p=%d", &memory, &passes, &lanes); err != nil {
		return 0, 0, 0, fmt.Errorf("parse params: %w", err)
	}

	switch {
	case lanes < 1 || lanes > maxArgonThreads:
		return 0, 0, 0, fmt.Errorf("%w: p=%d", ErrInvalidHashParams, lanes)
	case passes < 1 || passes > maxArgonTime:
		return 0, 0, 0, fmt.Errorf("%w: t=%d", ErrInvalidHashParams, passes)
	case memory < 8*lanes || memory > maxArgonMemory:
		return 0, 0, 0, fmt.Errorf("%w: m=%d", ErrInvalidHashParams, memory)
	}

	return memory, passes, uint8(lanes), nil //nolint:gosec // lanes <= maxArgonThreads
}
