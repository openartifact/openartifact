// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Key size bounds. Alpine's own keys are 4096 bits; apk accepts what OpenSSL
// can verify. The upper bound only stops a caller from asking for a key that
// takes minutes to generate or verify.
const (
	minKeyBits = 2048
	maxKeyBits = 16384

	keyNameSuffix = ".rsa.pub"
	// maxKeyNameLen keeps ".SIGN.RSA256.<key name>" within the 100 bytes of a
	// ustar header name, so a signature needs no PAX or GNU extension header,
	// which apk's tar reader does not have to understand.
	maxKeyNameLen = 100 - len(sigRSA256Prefix)

	pemPublic = "PUBLIC KEY"
	pemPKCS8  = "PRIVATE KEY"
	pemPKCS1  = "RSA PRIVATE KEY"
)

var (
	errKey    = errors.New("invalid signing key")
	errNotRSA = errors.New("not an RSA key")
)

// Signer signs indexes with an RSA key under the name apk looks the public key
// up by: the file name in /etc/apk/keys.
type Signer struct {
	name string
	key  *rsa.PrivateKey
	spki []byte
}

// validKeyName reports whether s is ^[A-Za-z0-9._@+-]+\.rsa\.pub$ and short enough
// for a ustar entry name.
func validKeyName(s string) bool {
	base, ok := strings.CutSuffix(s, keyNameSuffix)
	if !ok || base == "" || len(s) > maxKeyNameLen {
		return false
	}

	for i := range len(base) {
		c := base[i]
		if !isAlnum(c) && c != '.' && c != '_' && c != '@' && c != '+' && c != '-' {
			return false
		}
	}

	return true
}

// NewSigner returns a Signer for key. keyName must match
// ^[A-Za-z0-9._@+-]+\.rsa\.pub$ and be at most 87 bytes, and the key must be
// a valid RSA key of 2048 to 16384 bits.
func NewSigner(keyName string, key *rsa.PrivateKey) (*Signer, error) {
	if !validKeyName(keyName) {
		return nil, fmt.Errorf("%w: key name %q must match [A-Za-z0-9._@+-]+%s and be at most %d bytes",
			errKey, short(keyName), keyNameSuffix, maxKeyNameLen)
	}

	if err := checkPrivateKey(key); err != nil {
		return nil, err
	}

	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errKey, err)
	}

	return &Signer{name: keyName, key: key, spki: spki}, nil
}

func checkPrivateKey(key *rsa.PrivateKey) error {
	if key == nil || key.N == nil {
		return fmt.Errorf("%w: no key", errKey)
	}

	if bits := key.N.BitLen(); bits < minKeyBits || bits > maxKeyBits {
		return fmt.Errorf("%w: %d bits, want %d to %d", errKey, bits, minKeyBits, maxKeyBits)
	}

	if err := key.Validate(); err != nil {
		return fmt.Errorf("%w: %w", errKey, err)
	}

	return nil
}

// KeyName returns the name of the public key, as given to NewSigner.
func (s *Signer) KeyName() string { return s.name }

// PublicKeyPEM returns the public key as a "PUBLIC KEY" (SubjectPublicKeyInfo)
// PEM block: what apk keeps in /etc/apk/keys.
func (s *Signer) PublicKeyPEM() ([]byte, error) {
	out := pem.EncodeToMemory(&pem.Block{Type: pemPublic, Bytes: s.spki})
	if out == nil {
		return nil, fmt.Errorf("%w: cannot encode the public key", errKey)
	}

	return out, nil
}

// Sign returns the RSA PKCS#1 v1.5 signature of the SHA-256 of data: the
// content of a .SIGN.RSA256.<KeyName> entry, as [BuildIndex] writes it for an
// index.
func (s *Signer) Sign(data []byte) ([]byte, error) {
	sum := sha256.Sum256(data)

	return rsa.SignPKCS1v15(nil, s.key, crypto.SHA256, sum[:])
}

// Fingerprint returns the hex SHA-256 of the DER SubjectPublicKeyInfo.
func (s *Signer) Fingerprint() string {
	sum := sha256.Sum256(s.spki)

	return hex.EncodeToString(sum[:])
}

// GenerateKey generates an RSA key of bits bits, which must be between 2048
// and 16384.
func GenerateKey(bits int) (*rsa.PrivateKey, error) {
	if bits < minKeyBits || bits > maxKeyBits {
		return nil, fmt.Errorf("%w: %d bits, want %d to %d", errKey, bits, minKeyBits, maxKeyBits)
	}

	return rsa.GenerateKey(rand.Reader, bits)
}

// MarshalPrivateKeyPEM encodes k as a PKCS#8 "PRIVATE KEY" PEM block. It
// returns nil for a key that cannot be encoded.
func MarshalPrivateKeyPEM(k *rsa.PrivateKey) []byte {
	if k == nil {
		return nil
	}

	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil
	}

	return pem.EncodeToMemory(&pem.Block{Type: pemPKCS8, Bytes: der})
}

// ParsePrivateKeyPEM decodes the first PEM block of data as an unencrypted
// RSA private key of 2048 to 16384 bits: PKCS#8 ("PRIVATE KEY") or PKCS#1
// ("RSA PRIVATE KEY", what abuild-keygen writes).
func ParsePrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block", errKey)
	}

	if _, encrypted := block.Headers["DEK-Info"]; encrypted {
		return nil, fmt.Errorf("%w: encrypted keys are not supported", errKey)
	}

	var (
		key *rsa.PrivateKey
		err error
	)

	switch block.Type {
	case pemPKCS8:
		key, err = parsePKCS8(block.Bytes)
	case pemPKCS1:
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("%w: unsupported PEM type %q", errKey, short(block.Type))
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %w", errKey, err)
	}

	if err := checkPrivateKey(key); err != nil {
		return nil, err
	}

	return key, nil
}

func parsePKCS8(der []byte) (*rsa.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}

	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: %T", errNotRSA, parsed)
	}

	return key, nil
}
