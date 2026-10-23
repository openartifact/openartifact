// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpg

import (
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Algorithm names a key type OpenArtifact can generate.
type Algorithm string

const (
	// AlgorithmED25519 is an EdDSA key over Curve25519 (OpenPGP algorithm 22,
	// understood by GnuPG >= 2.1, apt's gpgv/sqv and dnf).
	AlgorithmED25519 Algorithm = "ed25519"
	// AlgorithmRSA4096 is a 4096-bit RSA key, the most widely compatible choice.
	AlgorithmRSA4096 Algorithm = "rsa4096"
)

// Sentinel errors for key parsing.
var (
	ErrUnsupportedAlgorithm = errors.New("unsupported key algorithm")
	ErrInvalidKey           = errors.New("not an ASCII-armored OpenPGP key")
	ErrNotPrivateKey        = errors.New("armored block does not contain a private key")
	ErrPassphraseRequired   = errors.New("private key is passphrase-protected")
	ErrBadPassphrase        = errors.New("wrong passphrase for private key")
	ErrKeyCannotSign        = errors.New("key cannot sign")
	ErrKeyRevoked           = errors.New("key is revoked")
	ErrKeyExpired           = errors.New("key is expired")
)

// KeyInfo is the public description of a signing key.
type KeyInfo struct {
	// Fingerprint is the upper-case hex fingerprint of the primary key.
	Fingerprint string
	// KeyID is the long (16 hex digit) key id.
	KeyID string
	// Algorithm is "ed25519", "rsa<bits>" or the OpenPGP algorithm name.
	Algorithm string
	// UserID is the primary user id ("Name (Comment) <email>"), if any.
	UserID string
	// ExpiresAt is nil for keys that never expire.
	ExpiresAt *time.Time
}

// ParseAlgorithm validates an algorithm name; the empty string selects ed25519.
func ParseAlgorithm(s string) (Algorithm, error) {
	switch Algorithm(strings.ToLower(strings.TrimSpace(s))) {
	case "", AlgorithmED25519:
		return AlgorithmED25519, nil
	case AlgorithmRSA4096:
		return AlgorithmRSA4096, nil
	default:
		return "", fmt.Errorf("%w: %q (use ed25519 or rsa4096)", ErrUnsupportedAlgorithm, s)
	}
}

// GenerateEntity generates a new key pair of the given type.
func GenerateEntity(name, comment, email string, alg Algorithm) (*openpgp.Entity, error) {
	config := &packet.Config{
		DefaultHash:   crypto.SHA256,
		DefaultCipher: packet.CipherAES256,
	}

	switch alg {
	case AlgorithmRSA4096:
		config.RSABits = 4096
	case AlgorithmED25519:
		config.Algorithm = packet.PubKeyAlgoEdDSA
		config.Curve = packet.Curve25519
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, alg)
	}

	entity, err := openpgp.NewEntity(name, comment, email, config)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	for _, id := range entity.Identities {
		err := id.SelfSignature.SignUserId(id.UserId.Id, entity.PrimaryKey, entity.PrivateKey, config)
		if err != nil {
			return nil, fmt.Errorf("failed to self-sign identity: %w", err)
		}
	}

	return entity, nil
}

// NewLocalSignerFromEntity wraps an entity that carries a decrypted private key.
func NewLocalSignerFromEntity(entity *openpgp.Entity) (*LocalSigner, error) {
	if err := checkSigningEntity(entity, time.Now()); err != nil {
		return nil, err
	}

	return &LocalSigner{entity: entity}, nil
}

// ParsePrivateKey reads the first key of an armored private key block, unlocks
// it with passphrase when it is protected, and verifies that it can sign and is
// neither revoked nor expired.
func ParsePrivateKey(armored io.Reader, passphrase []byte) (*LocalSigner, error) {
	entities, err := openpgp.ReadArmoredKeyRing(armored)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}

	if len(entities) == 0 {
		return nil, ErrNoKeysFound
	}

	entity := entities[0]

	if entity.PrivateKey == nil {
		return nil, ErrNotPrivateKey
	}

	if entity.PrivateKey.Encrypted {
		if len(passphrase) == 0 {
			return nil, ErrPassphraseRequired
		}

		if err := entity.DecryptPrivateKeys(passphrase); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadPassphrase, err)
		}
	}

	return NewLocalSignerFromEntity(entity)
}

func checkSigningEntity(entity *openpgp.Entity, now time.Time) error {
	if entity == nil || entity.PrivateKey == nil || entity.PrivateKey.Encrypted {
		return ErrNotPrivateKey
	}

	if !entity.PrimaryKey.PubKeyAlgo.CanSign() {
		return ErrKeyCannotSign
	}

	if entity.Revoked(now) {
		return ErrKeyRevoked
	}

	if exp := keyExpiry(entity); exp != nil && !exp.After(now) {
		return ErrKeyExpired
	}

	return nil
}

func keyExpiry(entity *openpgp.Entity) *time.Time {
	sig, _ := entity.PrimarySelfSignature()
	if sig == nil || sig.KeyLifetimeSecs == nil || *sig.KeyLifetimeSecs == 0 {
		return nil
	}

	exp := entity.PrimaryKey.CreationTime.Add(time.Duration(*sig.KeyLifetimeSecs) * time.Second)

	return &exp
}

// Info describes the key.
func (s *LocalSigner) Info() KeyInfo {
	pk := s.entity.PrimaryKey
	info := KeyInfo{
		Fingerprint: s.KeyFingerprint(),
		KeyID:       s.KeyID(),
		Algorithm:   algorithmName(pk),
		ExpiresAt:   keyExpiry(s.entity),
	}

	if _, ident := s.entity.PrimarySelfSignature(); ident != nil && ident.UserId != nil {
		info.UserID = ident.UserId.Id
	}

	return info
}

func algorithmName(pk *packet.PublicKey) string {
	switch pk.PubKeyAlgo {
	case packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSASignOnly:
		if bits, err := pk.BitLength(); err == nil {
			return fmt.Sprintf("rsa%d", bits)
		}

		return "rsa"
	case packet.PubKeyAlgoEdDSA, packet.PubKeyAlgoEd25519:
		return string(AlgorithmED25519)
	case packet.PubKeyAlgoECDSA:
		return "ecdsa"
	case packet.PubKeyAlgoDSA:
		return "dsa"
	default:
		return fmt.Sprintf("algo%d", pk.PubKeyAlgo)
	}
}

// ExportUnprotectedPrivateKey serialises the private key without passphrase
// protection, as an armored block. The caller must seal the result before it
// is stored anywhere.
func (s *LocalSigner) ExportUnprotectedPrivateKey() ([]byte, error) {
	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create armor encoder: %w", err)
	}

	if err := s.entity.SerializePrivateWithoutSigning(w, nil); err != nil {
		return nil, fmt.Errorf("failed to serialize private key: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("failed to finish armor: %w", err)
	}

	return buf.Bytes(), nil
}
