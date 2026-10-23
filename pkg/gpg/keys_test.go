// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpg

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

func armoredPrivate(t *testing.T, e *openpgp.Entity, passphrase string) string {
	t.Helper()

	if passphrase != "" {
		if err := e.PrivateKey.Encrypt([]byte(passphrase)); err != nil {
			t.Fatalf("encrypt primary: %v", err)
		}

		for _, sub := range e.Subkeys {
			if err := sub.PrivateKey.Encrypt([]byte(passphrase)); err != nil {
				t.Fatalf("encrypt subkey: %v", err)
			}
		}
	}

	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}

	_ = w.Close()

	return buf.String()
}

func TestGenerateEntityAlgorithms(t *testing.T) {
	tests := []struct {
		alg  Algorithm
		name string
	}{
		{AlgorithmED25519, "ed25519"},
		{AlgorithmRSA4096, "rsa4096"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := GenerateEntity("Repo Signer", "test", "signer@example.com", tt.alg)
			if err != nil {
				t.Fatalf("GenerateEntity: %v", err)
			}

			s, err := NewLocalSignerFromEntity(e)
			if err != nil {
				t.Fatalf("NewLocalSignerFromEntity: %v", err)
			}

			info := s.Info()
			if info.Algorithm != tt.name {
				t.Errorf("algorithm = %q, want %q", info.Algorithm, tt.name)
			}

			if !strings.Contains(info.UserID, "Repo Signer") || !strings.Contains(info.UserID, "signer@example.com") {
				t.Errorf("user id = %q", info.UserID)
			}

			data := "payload"

			sig, err := s.DetachedSign(strings.NewReader(data))
			if err != nil {
				t.Fatalf("DetachedSign: %v", err)
			}

			ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(mustPublic(t, s)))
			if err != nil {
				t.Fatal(err)
			}

			if _, err := openpgp.CheckArmoredDetachedSignature(ring, strings.NewReader(data), bytes.NewReader(sig), nil); err != nil {
				t.Errorf("signature does not verify against the exported public key: %v", err)
			}
		})
	}
}

func mustPublic(t *testing.T, s *LocalSigner) []byte {
	t.Helper()

	pub, err := s.PublicKey()
	if err != nil {
		t.Fatal(err)
	}

	return pub
}

func TestParseAlgorithm(t *testing.T) {
	for in, want := range map[string]Algorithm{"": AlgorithmED25519, "ED25519": AlgorithmED25519, "rsa4096": AlgorithmRSA4096} {
		got, err := ParseAlgorithm(in)
		if err != nil || got != want {
			t.Errorf("ParseAlgorithm(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	if _, err := ParseAlgorithm("dsa"); !errors.Is(err, ErrUnsupportedAlgorithm) {
		t.Errorf("ParseAlgorithm(dsa) err = %v, want ErrUnsupportedAlgorithm", err)
	}
}

func TestParsePrivateKeyPassphrase(t *testing.T) {
	e, err := GenerateEntity("Protected", "", "p@example.com", AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	fp := strings.ToUpper(hexFingerprint(e))
	armored := armoredPrivate(t, e, "s3cret")

	if _, err := ParsePrivateKey(strings.NewReader(armored), nil); !errors.Is(err, ErrPassphraseRequired) {
		t.Errorf("no passphrase: err = %v, want ErrPassphraseRequired", err)
	}

	if _, err := ParsePrivateKey(strings.NewReader(armored), []byte("wrong")); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("wrong passphrase: err = %v, want ErrBadPassphrase", err)
	}

	s, err := ParsePrivateKey(strings.NewReader(armored), []byte("s3cret"))
	if err != nil {
		t.Fatalf("right passphrase: %v", err)
	}

	if s.Info().Fingerprint != fp {
		t.Errorf("fingerprint = %s, want %s", s.Info().Fingerprint, fp)
	}

	// The unprotected export round-trips without a passphrase.
	plain, err := s.ExportUnprotectedPrivateKey()
	if err != nil {
		t.Fatalf("ExportUnprotectedPrivateKey: %v", err)
	}

	back, err := ParsePrivateKey(bytes.NewReader(plain), nil)
	if err != nil {
		t.Fatalf("re-importing the unprotected export: %v", err)
	}

	if back.Info().Fingerprint != fp {
		t.Errorf("round-trip fingerprint = %s, want %s", back.Info().Fingerprint, fp)
	}

	if _, err := back.DetachedSign(strings.NewReader("x")); err != nil {
		t.Errorf("round-tripped key cannot sign: %v", err)
	}
}

func hexFingerprint(e *openpgp.Entity) string {
	var b strings.Builder
	for _, c := range e.PrimaryKey.Fingerprint {
		b.WriteString(strings.ToUpper(string("0123456789abcdef"[c>>4])) + strings.ToUpper(string("0123456789abcdef"[c&0x0f])))
	}

	return b.String()
}

func TestParsePrivateKeyRejects(t *testing.T) {
	e, err := GenerateEntity("Pub", "", "", AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	var pub bytes.Buffer

	w, _ := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}

	_ = w.Close()

	if _, err := ParsePrivateKey(&pub, nil); !errors.Is(err, ErrNotPrivateKey) {
		t.Errorf("public key: err = %v, want ErrNotPrivateKey", err)
	}

	if _, err := ParsePrivateKey(strings.NewReader("not a key"), nil); err == nil {
		t.Error("garbage input was accepted")
	}
}

func TestNewLocalSignerFromEntityRejectsExpiredAndRevoked(t *testing.T) {
	expired, err := GenerateEntity("Old", "", "", AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	secs := uint32(1)
	for _, id := range expired.Identities {
		id.SelfSignature.KeyLifetimeSecs = &secs
	}

	expired.PrimaryKey.CreationTime = time.Now().Add(-48 * time.Hour)

	if _, err := NewLocalSignerFromEntity(expired); !errors.Is(err, ErrKeyExpired) {
		t.Errorf("expired key: err = %v, want ErrKeyExpired", err)
	}

	revoked, err := GenerateEntity("Revoked", "", "", AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	if err := revoked.RevokeKey(0, "test", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := NewLocalSignerFromEntity(revoked); !errors.Is(err, ErrKeyRevoked) {
		t.Errorf("revoked key: err = %v, want ErrKeyRevoked", err)
	}
}
