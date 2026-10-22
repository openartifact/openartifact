// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package crypto provides AES-256-GCM encryption of secrets stored at rest
// (webhook secrets, upstream passwords, SSO client secrets, AI API keys,
// storage backend credentials).
//
// The key comes from the OPENARTIFACT_SECRET_KEY environment variable (any
// string of at least 16 bytes; it is hashed with SHA-256 to a 32-byte key).
// Ciphertexts are encoded as "enc:v1:<base64(nonce||sealed)>". Values without
// that prefix are treated as legacy plaintext by Decrypt. When no key is
// configured, Encrypt returns the plaintext unchanged (and Enabled() is false),
// so deployments without a key keep working but store secrets unprotected.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
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

const prefix = "enc:v1:"

var (
	mu   sync.RWMutex //nolint:gochecknoglobals // guards the process-wide key cache
	aead cipher.AEAD  //nolint:gochecknoglobals // process-wide key cache initialised once from the environment
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
		aead = nil
		mu.Unlock()
		return nil
	}
	if len(key) < MinKeyLen {
		return fmt.Errorf("crypto: %s must be at least %d bytes: %w", EnvKey, MinKeyLen, errKeyTooShort)
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	mu.Lock()
	aead = g
	mu.Unlock()
	return nil
}

// InitFromEnv configures the key from OPENARTIFACT_SECRET_KEY.
func InitFromEnv() error { return Init(os.Getenv(EnvKey)) }

// Enabled reports whether a key is configured.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return aead != nil
}

// IsEncrypted reports whether s is an encrypted value produced by Encrypt.
func IsEncrypted(s string) bool { return strings.HasPrefix(s, prefix) }

// Encrypt seals plaintext. Empty input is returned unchanged; without a
// configured key the plaintext is returned unchanged. Input is always sealed,
// even when it already carries the "enc:v1:" prefix: passing such input
// through would let a caller smuggle a ciphertext into a field that is later
// decrypted and used (a decryption oracle). API boundaries must additionally
// reject prefixed input with ErrEncryptedInput.
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return plaintext, nil
	}
	mu.RLock()
	g := aead
	mu.RUnlock()
	if g == nil {
		return plaintext, nil
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := g.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt opens a value produced by Encrypt. Values without the "enc:v1:"
// prefix are legacy plaintext and returned as-is.
func Decrypt(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	mu.RLock()
	g := aead
	mu.RUnlock()
	if g == nil {
		return "", ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
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
