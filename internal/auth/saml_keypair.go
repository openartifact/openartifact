// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/pkg/logger"
)

// SettingSAMLSPKeypair is the system_settings key holding the generated SAML
// service-provider keypair: one value with the PEM private key followed by the
// PEM certificate (encrypted with internal/crypto when a secret key is set).
const SettingSAMLSPKeypair = "saml.sp_keypair"

const (
	pemRSAPrivateKey = "RSA PRIVATE KEY"
	pemCertificate   = "CERTIFICATE"
)

// ErrSAMLKeypairCorrupt is returned when the stored SP keypair cannot be
// parsed. It is never repaired by regenerating: a new certificate would
// silently break every IdP that already trusts the published one.
var ErrSAMLKeypairCorrupt = errors.New("stored saml sp keypair is corrupt")

type samlKeypairCache struct {
	mu   sync.Mutex
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

// spKeypair holds the database-backed keypair once read, so it is fetched from
// the settings store once per process.
//
//nolint:gochecknoglobals // process-lifetime cache of the persisted SP keypair
var spKeypair samlKeypairCache

func generateSelfSignedCert() (*rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "openartifact-saml-sp"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}

	return key, cert, nil
}

// loadOrGenerateSAMLKeypair returns the SP keypair. OPENARTIFACT_SAML_SP_CERT
// and OPENARTIFACT_SAML_SP_KEY win when both are set. Otherwise a self-signed
// RSA-2048 pair is created once and stored in system_settings, so restarts and
// every replica present the same certificate to the IdP.
func loadOrGenerateSAMLKeypair(ctx context.Context, store SettingsStore, cfg *config.Config, log *logger.Logger) (*rsa.PrivateKey, *x509.Certificate, error) {
	if cfg != nil && cfg.Auth.SAMLSPCertPEM != "" && cfg.Auth.SAMLSPKeyPEM != "" {
		key, cert, err := parseSAMLKeypair([]byte(cfg.Auth.SAMLSPKeyPEM), []byte(cfg.Auth.SAMLSPCertPEM))
		if err != nil {
			return nil, nil, fmt.Errorf("saml sp keypair from config: %w", err)
		}

		if log != nil {
			log.Infow("Loaded SAML SP keypair from environment variables", "cert_subject", cert.Subject.String())
		}

		return key, cert, nil
	}

	spKeypair.mu.Lock()
	defer spKeypair.mu.Unlock()

	if spKeypair.key != nil {
		return spKeypair.key, spKeypair.cert, nil
	}

	stored, err := loadOrCreateSetting(ctx, store, SettingSAMLSPKeypair, generateSAMLKeypairPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("persisted saml sp keypair: %w", err)
	}

	key, cert, err := splitSAMLKeypairPEM(stored)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrSAMLKeypairCorrupt, err)
	}

	if log != nil {
		log.Infow("SAML SP keypair not configured via environment variables; using the keypair stored in the database (shared by all replicas)",
			"cert_subject", cert.Subject.String())

		if !crypto.Enabled() {
			log.Warnw("OPENARTIFACT_SECRET_KEY not set; the generated SAML SP private key is stored unencrypted")
		}
	}

	spKeypair.key, spKeypair.cert = key, cert

	return key, cert, nil
}

// generateSAMLKeypairPEM creates a keypair serialized as one PEM bundle.
func generateSAMLKeypairPEM() (string, error) {
	key, cert, err := generateSelfSignedCert()
	if err != nil {
		return "", fmt.Errorf("generate saml sp keypair: %w", err)
	}

	var buf bytes.Buffer

	if err := pem.Encode(&buf, &pem.Block{Type: pemRSAPrivateKey, Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		return "", fmt.Errorf("encode saml sp key: %w", err)
	}

	if err := pem.Encode(&buf, &pem.Block{Type: pemCertificate, Bytes: cert.Raw}); err != nil {
		return "", fmt.Errorf("encode saml sp certificate: %w", err)
	}

	return buf.String(), nil
}

// splitSAMLKeypairPEM parses the bundle written by generateSAMLKeypairPEM.
func splitSAMLKeypairPEM(bundle string) (*rsa.PrivateKey, *x509.Certificate, error) {
	var keyPEM, certPEM []byte

	rest := []byte(bundle)

	for {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		encoded := pem.EncodeToMemory(block)

		switch block.Type {
		case pemRSAPrivateKey:
			keyPEM = encoded
		case pemCertificate:
			certPEM = encoded
		}
	}

	if keyPEM == nil || certPEM == nil {
		return nil, nil, errors.New("bundle must contain a private key and a certificate") //nolint:err113 // static error
	}

	return parseSAMLKeypair(keyPEM, certPEM)
}

// parseSAMLKeypair parses a PEM RSA private key (PKCS#1 or PKCS#8) and its
// certificate, and checks that they belong together.
func parseSAMLKeypair(keyPEM, certPEM []byte) (*rsa.PrivateKey, *x509.Certificate, error) {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, nil, fmt.Errorf("parse keypair: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("failed to decode private key PEM") //nolint:err113 // static error
	}

	privKey, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		parsed, err2 := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err2 != nil {
			return nil, nil, fmt.Errorf("parse private key: %w", err)
		}

		var ok bool

		if privKey, ok = parsed.(*rsa.PrivateKey); !ok {
			return nil, nil, errors.New("private key is not an RSA key") //nolint:err113 // static error
		}
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("failed to decode certificate PEM") //nolint:err113 // static error
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse certificate: %w", err)
	}

	return privKey, cert, nil
}
