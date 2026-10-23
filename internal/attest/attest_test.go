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
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	secretcrypto "github.com/openartifact/openartifact/internal/crypto"
)

const testSecret = "test-secret-key-0123456789-abcdef"

func testSubject() []Subject {
	sum := sha256.Sum256([]byte("package bytes"))
	return []Subject{SHA256Subject("pkg:npm/x@1.0.0", hex.EncodeToString(sum[:]))}
}

func signersUnderTest(t *testing.T) map[string]crypto.Signer {
	t.Helper()

	ec384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	ec256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	return map[string]crypto.Signer{"ecdsa-p256": ec256, "ecdsa-p384": ec384, "ed25519": ed}
}

func TestPAEMatchesSpecExample(t *testing.T) {
	got := string(PAE("http://example.com/HelloWorld", []byte("hello world")))
	if want := "DSSEv1 29 http://example.com/HelloWorld 11 hello world"; got != want {
		t.Fatalf("PAE = %q, want %q", got, want)
	}
}

func TestAttestRoundTripAndTamper(t *testing.T) {
	for name, key := range signersUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			s, err := NewSigner(key)
			if err != nil {
				t.Fatal(err)
			}

			if s.Algorithm() != name {
				t.Errorf("algorithm = %q", s.Algorithm())
			}

			env, err := s.Attest(PredicateOpenVEX, []byte(`{"statements":[]}`), testSubject())
			if err != nil {
				t.Fatal(err)
			}

			st, err := Verify(env, key.Public())
			if err != nil {
				t.Fatalf("verify: %v", err)
			}

			if st.Type != StatementType || st.PredicateType != PredicateOpenVEX || st.Subject[0].Name != "pkg:npm/x@1.0.0" {
				t.Errorf("statement = %+v", st)
			}

			// A different key and a modified payload must both fail.
			other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if _, err := Verify(env, other.Public()); !errors.Is(err, ErrBadSignature) {
				t.Errorf("wrong key accepted: %v", err)
			}

			env.Payload = env.Payload[:len(env.Payload)-4] + "AAAA"
			if _, err := Verify(env, key.Public()); err == nil {
				t.Error("tampered payload accepted")
			}
		})
	}
}

func TestAttestRejectsBadInput(t *testing.T) {
	k := signersUnderTest(t)["ed25519"]
	s, _ := NewSigner(k)

	if _, err := s.Attest(PredicateOpenVEX, []byte(`{`), testSubject()); !errors.Is(err, ErrInvalidPredicate) {
		t.Errorf("invalid predicate: %v", err)
	}

	if _, err := s.Attest(PredicateOpenVEX, []byte(`{}`), nil); !errors.Is(err, ErrNoSubject) {
		t.Errorf("no subject: %v", err)
	}
}

func TestEncodeBundleAndEnvelope(t *testing.T) {
	s, _ := NewSigner(signersUnderTest(t)["ecdsa-p256"])

	env, err := s.Attest(PredicateCycloneDX, []byte(`{"bomFormat":"CycloneDX"}`), testSubject())
	if err != nil {
		t.Fatal(err)
	}

	body, ct, err := s.Encode(env, false)
	if err != nil || ct != EnvelopeContentType {
		t.Fatalf("envelope: %v %q", err, ct)
	}

	var plain Envelope
	if err := json.Unmarshal(body, &plain); err != nil || plain.PayloadType != PayloadType {
		t.Fatalf("plain envelope = %+v %v", plain, err)
	}

	body, ct, err = s.Encode(env, true)
	if err != nil || ct != BundleContentType {
		t.Fatalf("bundle: %v %q", err, ct)
	}

	var b struct {
		MediaType            string `json:"mediaType"`
		VerificationMaterial struct {
			PublicKey struct{ Hint string } `json:"publicKey"`
		} `json:"verificationMaterial"`
		DSSEEnvelope Envelope `json:"dsseEnvelope"`
	}

	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}

	if b.MediaType != BundleMediaType || b.VerificationMaterial.PublicKey.Hint != s.KeyID() || b.DSSEEnvelope.Payload != env.Payload {
		t.Errorf("bundle = %+v", b)
	}
}

func TestPublicKeyPEMIsPKIX(t *testing.T) {
	s, _ := NewSigner(signersUnderTest(t)["ecdsa-p256"])

	blk, _ := pem.Decode(s.PublicKeyPEM())
	if blk == nil || blk.Type != "PUBLIC KEY" {
		t.Fatalf("pem = %q", s.PublicKeyPEM())
	}

	if _, err := x509.ParsePKIXPublicKey(blk.Bytes); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(blk.Bytes)
	if s.KeyID() != hex.EncodeToString(sum[:]) {
		t.Errorf("key id = %s", s.KeyID())
	}
}

func TestServiceLoadsKeyFile(t *testing.T) {
	dir := t.TempDir()

	_, ed, _ := ed25519.GenerateKey(rand.Reader)

	der, err := x509.MarshalPKCS8PrivateKey(ed)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(path, pemBlock("PRIVATE KEY", der), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := NewService(path, nil).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if info.Source != SourceFile || info.Algorithm != "ed25519" {
		t.Errorf("info = %+v", info)
	}

	// SEC1 "EC PRIVATE KEY" works too.
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sec1, _ := x509.MarshalECPrivateKey(ec)

	if err := os.WriteFile(path, pemBlock("EC PRIVATE KEY", sec1), 0o600); err != nil {
		t.Fatal(err)
	}

	if info, err = NewService(path, nil).Info(context.Background()); err != nil || info.Algorithm != "ecdsa-p256" {
		t.Errorf("sec1: %+v %v", info, err)
	}

	for name, content := range map[string]string{"garbage": "not pem", "rsa-like": string(pemBlock("PUBLIC KEY", []byte("x")))} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := NewService(path, nil).Signer(context.Background()); err == nil {
			t.Errorf("%s key file accepted", name)
		}
	}

	if _, err := NewService(filepath.Join(dir, "missing.pem"), nil).Signer(context.Background()); err == nil {
		t.Error("missing key file accepted")
	}
}

type memSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSettings) GetSystemSetting(_ context.Context, k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.m[k], nil
}

func (s *memSettings) CreateSystemSettingIfAbsent(_ context.Context, k, v string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.m[k]; ok {
		return false, nil
	}

	s.m[k] = v

	return true, nil
}

func TestServiceGeneratesEncryptedKeyOnceAcrossReplicas(t *testing.T) {
	if err := secretcrypto.Init(testSecret); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = secretcrypto.Init("") })

	store := &memSettings{m: map[string]string{}}

	var wg sync.WaitGroup

	ids := make([]string, 8)

	for i := range ids {
		wg.Go(func() {
			// Each service is a separate replica sharing the database.
			info, err := NewService("", store).Info(context.Background())
			if err != nil {
				t.Error(err)
				return
			}

			ids[i] = info.KeyID
		})
	}

	wg.Wait()

	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("replicas disagree on the key: %v", ids)
		}
	}

	stored := store.m[SettingPrivateKey]
	if !secretcrypto.IsEncrypted(stored) || strings.Contains(stored, "PRIVATE KEY") {
		t.Errorf("private key is not stored encrypted: %.30q", stored)
	}

	// A restart (new service) reuses the stored key.
	info, err := NewService("", store).Info(context.Background())
	if err != nil || info.KeyID != ids[0] || info.Source != SourceGenerated {
		t.Errorf("restart: %+v %v", info, err)
	}
}

func TestServiceWithoutKeyOrStoreFails(t *testing.T) {
	if _, err := NewService("", nil).Signer(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}
