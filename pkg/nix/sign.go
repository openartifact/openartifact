// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// maxKeyNameLen bounds the name of a signing key.
const maxKeyNameLen = 160

// ErrInvalidKey is returned for a key that is not in Nix's "<name>:<base64>"
// format or does not hold a consistent ed25519 key pair.
var ErrInvalidKey = errors.New("invalid nix signing key")

// SecretKey is an ed25519 key that signs narinfo. Its text form is the one
// "nix-store --generate-binary-cache-key" writes: "<name>:<base64 of seed and
// public key>".
type SecretKey struct {
	name string
	key  ed25519.PrivateKey
}

// ValidKeyName reports whether s can name a key: it is the part before the
// colon in trusted-public-keys and signatures.
func ValidKeyName(s string) bool {
	if s == "" || len(s) > maxKeyNameLen {
		return false
	}

	for i := range len(s) {
		if !validNameChar(s[i]) {
			return false
		}
	}

	return true
}

// GenerateKey creates a key with a fresh random pair.
func GenerateKey(name string) (*SecretKey, error) {
	if !ValidKeyName(name) {
		return nil, fmt.Errorf("%w: bad key name", ErrInvalidKey)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	return &SecretKey{name: name, key: priv}, nil
}

// ParseSecretKey reads the text form of a secret key.
func ParseSecretKey(s string) (*SecretKey, error) {
	name, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || !ValidKeyName(name) {
		return nil, ErrInvalidKey
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, ErrInvalidKey
	}

	priv := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
	if string(priv) != string(raw) {
		return nil, fmt.Errorf("%w: public half does not match the seed", ErrInvalidKey)
	}

	return &SecretKey{name: name, key: priv}, nil
}

// Name returns the key name.
func (k *SecretKey) Name() string { return k.name }

// String returns the secret key in its text form. Callers seal it before
// storing it and never log it.
func (k *SecretKey) String() string {
	return k.name + ":" + base64.StdEncoding.EncodeToString(k.key)
}

// PublicKey returns the key to put into trusted-public-keys: "<name>:<base64
// of the public key>".
func (k *SecretKey) PublicKey() string {
	pub, _ := k.key.Public().(ed25519.PublicKey)

	return k.name + ":" + base64.StdEncoding.EncodeToString(pub)
}

// Sign returns the signature of the narinfo, "<name>:<base64>".
func (k *SecretKey) Sign(n *NarInfo) (string, error) {
	fp, err := n.Fingerprint()
	if err != nil {
		return "", err
	}

	return k.name + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(k.key, []byte(fp))), nil
}

// Renamed returns the same key pair under another name.
func (k *SecretKey) Renamed(name string) (*SecretKey, error) {
	if !ValidKeyName(name) {
		return nil, fmt.Errorf("%w: bad key name", ErrInvalidKey)
	}

	return &SecretKey{name: name, key: k.key}, nil
}
