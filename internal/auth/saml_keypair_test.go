// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/crypto"
)

// newReplica forgets the process-level cache, as a fresh replica starts with.
func newReplica(t *testing.T) {
	t.Helper()

	reset := func() {
		spKeypair.mu.Lock()
		spKeypair.key, spKeypair.cert = nil, nil
		spKeypair.mu.Unlock()
	}

	reset()
	t.Cleanup(reset)
}

func TestSAMLKeypair_SharedAcrossReplicas(t *testing.T) {
	store := &keySettingsStore{m: map[string]string{}}
	ctx := context.Background()

	newReplica(t)

	_, certA, err := loadOrGenerateSAMLKeypair(ctx, store, &config.Config{}, nil)
	if err != nil {
		t.Fatalf("first replica: %v", err)
	}

	if store.m[SettingSAMLSPKeypair] == "" {
		t.Fatal("the generated keypair must be persisted in system_settings")
	}

	// Same process: served from memory, store untouched.
	keyAgain, certAgain, err := loadOrGenerateSAMLKeypair(ctx, store, &config.Config{}, nil)
	if err != nil || !bytes.Equal(certAgain.Raw, certA.Raw) || keyAgain == nil {
		t.Fatalf("repeat load: err=%v", err)
	}

	// Another replica (empty process cache, same database) adopts the stored pair.
	newReplica(t)

	keyB, certB, err := loadOrGenerateSAMLKeypair(ctx, store, &config.Config{}, nil)
	if err != nil {
		t.Fatalf("second replica: %v", err)
	}

	if !bytes.Equal(certA.Raw, certB.Raw) {
		t.Error("replicas must present the same SP certificate")
	}

	if !keyB.PublicKey.Equal(certB.PublicKey) {
		t.Error("the stored private key must match the stored certificate")
	}

	if len(store.m) != 1 {
		t.Errorf("exactly one setting expected, got %d", len(store.m))
	}
}

func TestSAMLKeypair_EnvironmentWinsAndNeverTouchesTheStore(t *testing.T) {
	newReplica(t)

	bundle, err := generateSAMLKeypairPEM()
	if err != nil {
		t.Fatal(err)
	}

	keyPEM, certPEM := splitPEM(t, bundle)
	store := &keySettingsStore{m: map[string]string{}}
	cfg := &config.Config{}
	cfg.Auth.SAMLSPKeyPEM, cfg.Auth.SAMLSPCertPEM = keyPEM, certPEM

	_, cert, err := loadOrGenerateSAMLKeypair(context.Background(), store, cfg, nil)
	if err != nil {
		t.Fatalf("env keypair: %v", err)
	}

	want := parseCertPEM(t, certPEM)
	if !bytes.Equal(cert.Raw, want.Raw) {
		t.Error("the environment certificate must be used")
	}

	if len(store.m) != 0 {
		t.Error("an environment keypair must not be persisted")
	}

	// Even with a stored pair present, the environment still wins.
	store.m[SettingSAMLSPKeypair] = bundle

	other, err := generateSAMLKeypairPEM()
	if err != nil {
		t.Fatal(err)
	}

	otherKey, otherCert := splitPEM(t, other)
	cfg.Auth.SAMLSPKeyPEM, cfg.Auth.SAMLSPCertPEM = otherKey, otherCert

	_, cert, err = loadOrGenerateSAMLKeypair(context.Background(), store, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	if want = parseCertPEM(t, otherCert); !bytes.Equal(cert.Raw, want.Raw) {
		t.Error("the environment keypair must win over the stored one")
	}
}

func TestSAMLKeypair_RejectsBadInput(t *testing.T) {
	bundle, err := generateSAMLKeypairPEM()
	if err != nil {
		t.Fatal(err)
	}

	otherBundle, err := generateSAMLKeypairPEM()
	if err != nil {
		t.Fatal(err)
	}

	keyPEM, _ := splitPEM(t, bundle)
	_, otherCertPEM := splitPEM(t, otherBundle)

	tests := []struct {
		name     string
		cfgKey   string
		cfgCert  string
		stored   string
		store    SettingsStore
		wantCorr bool // want ErrSAMLKeypairCorrupt
		wantNoSt bool // want ErrNoSettingsStore
	}{
		{name: "env key does not match env cert", cfgKey: keyPEM, cfgCert: otherCertPEM},
		{name: "env values are not PEM", cfgKey: "nope", cfgCert: "nope"},
		{name: "stored value is garbage", stored: "garbage", wantCorr: true},
		{name: "stored value has no certificate", stored: keyPEM, wantCorr: true},
		{name: "stored key and certificate do not match", stored: keyPEM + otherCertPEM, wantCorr: true},
		{name: "no settings store", store: nil, wantNoSt: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newReplica(t)

			kv := &keySettingsStore{m: map[string]string{}}
			if tt.stored != "" {
				kv.m[SettingSAMLSPKeypair] = tt.stored
			}

			var store SettingsStore = kv
			if tt.wantNoSt {
				store = nil
			}

			cfg := &config.Config{}
			cfg.Auth.SAMLSPKeyPEM, cfg.Auth.SAMLSPCertPEM = tt.cfgKey, tt.cfgCert

			_, _, err := loadOrGenerateSAMLKeypair(context.Background(), store, cfg, nil)
			if err == nil {
				t.Fatal("expected an error")
			}

			if tt.wantCorr && !errors.Is(err, ErrSAMLKeypairCorrupt) {
				t.Errorf("expected ErrSAMLKeypairCorrupt, got %v", err)
			}

			if tt.wantNoSt && !errors.Is(err, ErrNoSettingsStore) {
				t.Errorf("expected ErrNoSettingsStore, got %v", err)
			}

			// A corrupt stored pair is never replaced: a silent regeneration would
			// break every IdP that already trusts the published certificate.
			if tt.stored != "" && kv.m[SettingSAMLSPKeypair] != tt.stored {
				t.Error("a corrupt stored keypair must not be overwritten")
			}
		})
	}
}

func TestSAMLKeypair_StoredEncryptedWhenSecretKeySet(t *testing.T) {
	if err := crypto.Init(strings.Repeat("k", 40)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	newReplica(t)

	store := &keySettingsStore{m: map[string]string{}}

	_, certA, err := loadOrGenerateSAMLKeypair(context.Background(), store, &config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	raw := store.m[SettingSAMLSPKeypair]
	if !crypto.IsEncrypted(raw) || strings.Contains(raw, "PRIVATE KEY") {
		t.Fatalf("the stored private key must be encrypted, got %.20q", raw)
	}

	newReplica(t)

	_, certB, err := loadOrGenerateSAMLKeypair(context.Background(), store, &config.Config{}, nil)
	if err != nil || !bytes.Equal(certA.Raw, certB.Raw) {
		t.Fatalf("encrypted keypair must round-trip: %v", err)
	}
}

func splitPEM(t *testing.T, bundle string) (string, string) {
	t.Helper()

	const marker = "-----BEGIN CERTIFICATE-----"

	i := strings.Index(bundle, marker)
	if i < 0 {
		t.Fatal("bundle has no certificate")
	}

	return bundle[:i], bundle[i:]
}

func parseCertPEM(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()

	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("not a PEM certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	return cert
}
