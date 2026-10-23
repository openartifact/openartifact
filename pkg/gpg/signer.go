// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpg

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Sentinel errors for GPG operations.
var (
	ErrNoKeysFound = errors.New("no keys found in keyring")
)

// Signer defines the interface for GPG signing operations.
type Signer interface {
	// CleartextSign produces an InRelease-style cleartext signature
	CleartextSign(data io.Reader) (io.Reader, error)
	// DetachedSign produces a Release.gpg-style detached signature
	DetachedSign(data io.Reader) ([]byte, error)
	// PublicKey returns the ASCII-armored public key for client distribution
	PublicKey() ([]byte, error)
	// KeyFingerprint returns the key fingerprint
	KeyFingerprint() string
}

// Resolver picks the signer for a repository. A nil Signer with a nil error
// means the repository is not signed.
type Resolver interface {
	SignerForRepo(ctx context.Context, repoID string) (Signer, error)
}

// LocalSigner implements Signer using local GPG keys.
type LocalSigner struct {
	entity *openpgp.Entity
}

// NewLocalSigner creates a signer from an existing private key.
func NewLocalSigner(keyReader io.Reader, passphrase []byte) (*LocalSigner, error) {
	entityList, err := openpgp.ReadArmoredKeyRing(keyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read armored key: %w", err)
	}

	if len(entityList) == 0 {
		return nil, ErrNoKeysFound
	}

	entity := entityList[0]

	if len(passphrase) > 0 && entity.PrivateKey != nil && entity.PrivateKey.Encrypted {
		err := entity.PrivateKey.Decrypt(passphrase)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt private key: %w", err)
		}

		for _, subkey := range entity.Subkeys {
			if subkey.PrivateKey != nil && subkey.PrivateKey.Encrypted {
				err := subkey.PrivateKey.Decrypt(passphrase)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt subkey: %w", err)
				}
			}
		}
	}

	return &LocalSigner{entity: entity}, nil
}

// NewLocalSignerFromFile creates a signer from a key file.
func NewLocalSignerFromFile(keyPath string, passphrase []byte) (*LocalSigner, error) {
	file, err := os.Open(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open key file: %w", err)
	}
	defer func() { _ = file.Close() }()

	return NewLocalSigner(file, passphrase)
}

// GenerateKey generates a new RSA-4096 PGP key pair.
func GenerateKey(name, comment, email string) (*openpgp.Entity, error) {
	return GenerateEntity(name, comment, email, AlgorithmRSA4096)
}

// CleartextSign implements Signer.CleartextSign.
func (s *LocalSigner) CleartextSign(data io.Reader) (io.Reader, error) {
	var msgBuf bytes.Buffer
	if _, err := io.Copy(&msgBuf, data); err != nil {
		return nil, fmt.Errorf("failed to buffer data: %w", err)
	}

	// clearsign.Encode performs dash-escaping, line-ending canonicalisation
	// and emits a text-mode (0x01) signature, as required for InRelease.
	var result bytes.Buffer

	cw, err := clearsign.Encode(&result, s.entity.PrivateKey, &packet.Config{DefaultHash: crypto.SHA256})
	if err != nil {
		return nil, fmt.Errorf("failed to start clearsign: %w", err)
	}

	if _, err := cw.Write(msgBuf.Bytes()); err != nil {
		return nil, fmt.Errorf("failed to write clearsign body: %w", err)
	}

	if err := cw.Close(); err != nil {
		return nil, fmt.Errorf("failed to finish clearsign: %w", err)
	}

	return bytes.NewReader(result.Bytes()), nil
}

// DetachedSign implements Signer.DetachedSign.
func (s *LocalSigner) DetachedSign(data io.Reader) ([]byte, error) {
	var buf bytes.Buffer

	// ArmoredDetachSign already wraps the signature in an armor block; adding
	// another armor.Encode layer makes Release.gpg/repomd.xml.asc unverifiable.
	err := openpgp.ArmoredDetachSign(&buf, s.entity, data, &packet.Config{DefaultHash: crypto.SHA256})
	if err != nil {
		return nil, fmt.Errorf("failed to create detached signature: %w", err)
	}

	return buf.Bytes(), nil
}

// PublicKey implements Signer.PublicKey.
func (s *LocalSigner) PublicKey() ([]byte, error) {
	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create armor encoder: %w", err)
	}

	err = s.entity.Serialize(w)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize public key: %w", err)
	}

	_ = w.Close()

	return buf.Bytes(), nil
}

// KeyFingerprint implements Signer.KeyFingerprint.
func (s *LocalSigner) KeyFingerprint() string {
	if s.entity == nil || s.entity.PrimaryKey == nil {
		return ""
	}

	return fmt.Sprintf("%X", s.entity.PrimaryKey.Fingerprint)
}

// SigningKey returns the unlocked private key that makes signatures: the
// signing subkey when the entity has one, the primary key otherwise. It lets
// signers of other formats (RPM package signatures) use the same key.
func (s *LocalSigner) SigningKey() (*packet.PrivateKey, error) {
	if s.entity == nil {
		return nil, ErrNoKeysFound
	}

	key, ok := s.entity.SigningKey(time.Now())
	if !ok || key.PrivateKey == nil || key.PrivateKey.Encrypted {
		return nil, ErrKeyCannotSign
	}

	return key.PrivateKey, nil
}

// KeyID returns the short key ID (last 16 hex characters).
func (s *LocalSigner) KeyID() string {
	fp := s.KeyFingerprint()
	if len(fp) < 16 {
		return fp
	}

	return fp[len(fp)-16:]
}

// ExportPrivateKey exports the private key in armored format.
func (s *LocalSigner) ExportPrivateKey() ([]byte, error) {
	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create armor encoder: %w", err)
	}

	err = s.entity.SerializePrivate(w, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize private key: %w", err)
	}

	_ = w.Close()

	return buf.Bytes(), nil
}
