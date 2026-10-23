// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync"

	secretcrypto "github.com/openartifact/openartifact/internal/crypto"
)

// SettingPrivateKey is the system_settings key holding the generated signing
// key (PKCS#8 PEM, encrypted with internal/crypto when a secret key is set).
const SettingPrivateKey = "attestation.private_key"

// Key sources reported by Service.Info.
const (
	SourceFile      = "file"
	SourceGenerated = "generated"
)

// ErrUnsupportedKey is returned for keys that are neither ECDSA nor Ed25519.
var ErrUnsupportedKey = errors.New("attestation key must be ECDSA or Ed25519")

// ErrNoKeyBlock is returned when a key file holds no PEM private key.
var ErrNoKeyBlock = errors.New("no PEM private key found")

// SettingsStore is the part of the database the generated key needs.
type SettingsStore interface {
	GetSystemSetting(ctx context.Context, key string) (string, error)
	CreateSystemSettingIfAbsent(ctx context.Context, key, value string) (bool, error)
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// ParsePrivateKeyPEM parses a PKCS#8, SEC1 (EC) PEM private key into an ECDSA
// or Ed25519 signer.
func ParsePrivateKeyPEM(data []byte) (crypto.Signer, error) {
	for {
		var blk *pem.Block

		blk, data = pem.Decode(data)
		if blk == nil {
			return nil, ErrNoKeyBlock
		}

		switch blk.Type {
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse PKCS#8 key: %w", err)
			}

			switch s := k.(type) {
			case *ecdsa.PrivateKey:
				return s, nil
			case ed25519.PrivateKey:
				return s, nil
			default:
				return nil, fmt.Errorf("%w: %T", ErrUnsupportedKey, k)
			}
		case "EC PRIVATE KEY":
			k, err := x509.ParseECPrivateKey(blk.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse EC key: %w", err)
			}

			return k, nil
		}
	}
}

// GenerateKeyPEM creates a new ECDSA P-256 key (the cosign default) and returns it as PKCS#8 PEM.
func GenerateKeyPEM() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}

	return pemBlock("PRIVATE KEY", der), nil
}

// Info describes the active signing key.
type Info struct {
	KeyID        string
	Algorithm    string
	Source       string
	PublicKeyPEM string
}

// Service resolves the signing key lazily: from the configured key file, else
// from the database, generating and storing one on first use.
type Service struct {
	keyPath string
	store   SettingsStore

	mu     sync.Mutex
	signer *Signer
	source string
}

// NewService creates a Service. keyPath may be empty (generated key); store may be nil only when keyPath is set.
func NewService(keyPath string, store SettingsStore) *Service {
	return &Service{keyPath: keyPath, store: store}
}

// Signer returns the signing key, loading or generating it on first use.
func (s *Service) Signer(ctx context.Context) (*Signer, error) {
	sg, _, err := s.resolve(ctx)
	return sg, err
}

// Info returns the key's identity and public key.
func (s *Service) Info(ctx context.Context) (*Info, error) {
	sg, src, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}

	return &Info{KeyID: sg.KeyID(), Algorithm: sg.Algorithm(), Source: src, PublicKeyPEM: string(sg.PublicKeyPEM())}, nil
}

func (s *Service) resolve(ctx context.Context) (*Signer, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.signer != nil {
		return s.signer, s.source, nil
	}

	if s.keyPath != "" {
		data, err := os.ReadFile(s.keyPath)
		if err != nil {
			return nil, "", fmt.Errorf("read attestation key: %w", err)
		}

		sg, err := signerFromPEM(data)
		if err != nil {
			return nil, "", err
		}

		s.signer, s.source = sg, SourceFile

		return sg, SourceFile, nil
	}

	sg, err := s.loadOrGenerate(ctx)
	if err != nil {
		return nil, "", err
	}

	s.signer, s.source = sg, SourceGenerated

	return sg, SourceGenerated, nil
}

func signerFromPEM(data []byte) (*Signer, error) {
	k, err := ParsePrivateKeyPEM(data)
	if err != nil {
		return nil, err
	}

	return NewSigner(k)
}

// loadOrGenerate reads the stored key; with none, it generates one and stores
// it unless another replica stored one first, in which case that one wins.
func (s *Service) loadOrGenerate(ctx context.Context) (*Signer, error) {
	if s.store == nil {
		return nil, errors.New("attestation: no key file configured and no database") //nolint:err113 // wiring error
	}

	stored, err := s.store.GetSystemSetting(ctx, SettingPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("read attestation key: %w", err)
	}

	if stored == "" {
		pemKey, err := GenerateKeyPEM()
		if err != nil {
			return nil, err
		}

		enc, err := secretcrypto.Encrypt(string(pemKey))
		if err != nil {
			return nil, fmt.Errorf("encrypt attestation key: %w", err)
		}

		if _, err := s.store.CreateSystemSettingIfAbsent(ctx, SettingPrivateKey, enc); err != nil {
			return nil, fmt.Errorf("store attestation key: %w", err)
		}

		// Re-read: when two replicas raced, both must use the stored winner.
		if stored, err = s.store.GetSystemSetting(ctx, SettingPrivateKey); err != nil {
			return nil, fmt.Errorf("read attestation key: %w", err)
		}
	}

	plain, err := secretcrypto.Decrypt(stored)
	if err != nil {
		return nil, fmt.Errorf("decrypt attestation key: %w", err)
	}

	return signerFromPEM([]byte(plain))
}
