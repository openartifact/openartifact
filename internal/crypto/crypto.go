// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package crypto provides AES-256-GCM encryption of secrets stored at rest
// (webhook secrets, upstream passwords, SSO client secrets, AI API keys,
// storage backend credentials).
//
// The key comes from the OPENARTIFACT_SECRET_KEY environment variable (any
// string of at least 16 bytes). Two ciphertext generations exist, both encoded
// as "<prefix><base64(nonce||sealed)>":
//
//   - "enc:v2:" (written by Encrypt): the AES-256 key is derived with
//     HKDF-SHA256 (RFC 5869) from the configured secret, using the fixed salt
//     [hkdfSalt] and the context string [hkdfInfo]. The salt is a constant
//     because the secret is the only per-deployment input; it separates this
//     derivation from any other use of the same string.
//   - "enc:v1:" (legacy, read-only): the AES-256 key is the plain SHA-256 of
//     the configured secret. Decrypt still opens these transparently; values
//     are re-sealed as v2 the next time they are saved. There is no forced
//     migration.
//
// An older binary that only knows "enc:v1:" cannot read "enc:v2:" values, so
// downgrading after v2 values were written requires re-entering those secrets.
//
// HKDF is a key-derivation function, not a password hash: it does not slow
// down guessing, so the secret should be high-entropy (for example
// `openssl rand -base64 32`).
//
// Values without a known prefix are treated as legacy plaintext by Decrypt.
// When no key is configured, Encrypt returns the plaintext unchanged (and
// Enabled() is false), so deployments without a key keep working but store
// secrets unprotected.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// EnvKey is the environment variable holding the secret-encryption key.
const EnvKey = "OPENARTIFACT_SECRET_KEY"

// MinKeyLen is the minimum accepted key length in bytes.
const MinKeyLen = 16

const (
	prefixV1 = "enc:v1:"
	prefixV2 = "enc:v2:"

	// hkdfSalt and hkdfInfo are fixed, public domain-separation constants for
	// the enc:v2 key derivation. Changing either makes every enc:v2 value
	// undecryptable, so they must never change.
	hkdfSalt = "openartifact/secret-key/salt/v2"
	hkdfInfo = "openartifact enc:v2 AES-256-GCM"

	aesKeyLen = 32
)

// ciphers holds the AEAD for each ciphertext generation, derived from one key.
type ciphers struct {
	v1 cipher.AEAD // SHA-256 key, decrypt only
	v2 cipher.AEAD // HKDF-SHA256 key, used for new encryptions
}

var (
	mu  sync.RWMutex //nolint:gochecknoglobals // guards the process-wide key cache
	cur *ciphers     //nolint:gochecknoglobals // process-wide key cache initialised once from the environment
)

var (
	errKeyTooShort        = errors.New("key too short")
	errCiphertextTooShort = errors.New("ciphertext too short")
)

// ErrNoKey is returned by Decrypt when ciphertext is present but no key is configured.
var ErrNoKey = errors.New("crypto: encrypted value present but " + EnvKey + " is not set")

// ErrEncryptedInput is returned by CheckPlaintext for values that already carry
// the ciphertext prefix.
var ErrEncryptedInput = errors.New("crypto: value must be plaintext, not an encrypted value")

// CheckPlaintext rejects user-supplied secret input that looks like a
// ciphertext produced by Encrypt. Call it at every API boundary that accepts a
// secret to be stored encrypted.
func CheckPlaintext(s string) error {
	if IsEncrypted(s) {
		return ErrEncryptedInput
	}

	return nil
}

// Init configures the package-level key. An empty key disables encryption.
func Init(key string) error {
	if key == "" {
		mu.Lock()
		cur = nil
		mu.Unlock()
		return nil
	}
	if len(key) < MinKeyLen {
		return fmt.Errorf("crypto: %s must be at least %d bytes: %w", EnvKey, MinKeyLen, errKeyTooShort)
	}
	sum := sha256.Sum256([]byte(key))
	v1, err := newGCM(sum[:])
	if err != nil {
		return err
	}
	derived, err := hkdf.Key(sha256.New, []byte(key), []byte(hkdfSalt), hkdfInfo, aesKeyLen)
	if err != nil {
		return fmt.Errorf("crypto: derive key: %w", err)
	}
	v2, err := newGCM(derived)
	if err != nil {
		return err
	}
	mu.Lock()
	cur = &ciphers{v1: v1, v2: v2}
	mu.Unlock()
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// InitFromEnv configures the key from OPENARTIFACT_SECRET_KEY.
func InitFromEnv() error { return Init(os.Getenv(EnvKey)) }

// Enabled reports whether a key is configured.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return cur != nil
}

// IsEncrypted reports whether s is an encrypted value produced by Encrypt.
// Both the enc:v1: and enc:v2: generations are recognised.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, prefixV2) || strings.HasPrefix(s, prefixV1)
}

// Encrypt seals plaintext as an "enc:v2:" value. Empty input is returned
// unchanged; without a configured key the plaintext is returned unchanged.
// Input is always sealed, even when it already carries an "enc:" prefix:
// passing such input through would let a caller smuggle a ciphertext into a
// field that is later decrypted and used (a decryption oracle). API boundaries
// must additionally reject prefixed input with ErrEncryptedInput.
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return plaintext, nil
	}
	mu.RLock()
	c := cur
	mu.RUnlock()
	if c == nil {
		return plaintext, nil
	}
	nonce := make([]byte, c.v2.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.v2.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefixV2 + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt opens an "enc:v2:" or legacy "enc:v1:" value produced by Encrypt.
// Values without a known prefix are legacy plaintext and returned as-is.
func Decrypt(value string) (string, error) {
	var (
		body string
		pick func(*ciphers) cipher.AEAD
	)
	switch {
	case strings.HasPrefix(value, prefixV2):
		body, pick = strings.TrimPrefix(value, prefixV2), func(c *ciphers) cipher.AEAD { return c.v2 }
	case strings.HasPrefix(value, prefixV1):
		body, pick = strings.TrimPrefix(value, prefixV1), func(c *ciphers) cipher.AEAD { return c.v1 }
	default:
		return value, nil
	}
	mu.RLock()
	c := cur
	mu.RUnlock()
	if c == nil {
		return "", ErrNoKey
	}
	g := pick(c)
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("crypto: decode: %w", err)
	}
	if len(raw) < g.NonceSize() {
		return "", fmt.Errorf("crypto: %w", errCiphertextTooShort)
	}
	pt, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("crypto: decrypt: %w", err)
	}
	return string(pt), nil
}
