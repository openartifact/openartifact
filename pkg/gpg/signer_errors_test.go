// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpg

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func newEntity(t *testing.T) *openpgp.Entity {
	t.Helper()

	e, err := GenerateEntity("Err Test", "", "err@example.com", AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func TestNewLocalSignerKeyHandling(t *testing.T) {

	protected := armoredPrivate(t, newEntity(t), "s3cret")
	plain := armoredPrivate(t, newEntity(t), "")
	public := mustPublic(t, mustSigner(t, newEntity(t)))

	tests := []struct {
		name       string
		key        string
		passphrase string
		wantErr    bool
		wantSign   bool
		wantIs     error
	}{
		{name: "plain key", key: plain, wantSign: true},
		{name: "protected key right passphrase", key: protected, passphrase: "s3cret", wantSign: true},
		{name: "protected key wrong passphrase", key: protected, passphrase: "nope", wantErr: true},
		{name: "protected key without passphrase", key: protected, wantErr: true, wantIs: ErrPassphraseRequired},
		{name: "binary garbage", key: "\x00\x01\x02 not armor", wantErr: true},
		{name: "empty input", key: "", wantErr: true},
		{name: "public key", key: string(public), wantErr: true, wantIs: ErrNotPrivateKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			s, err := NewLocalSigner(strings.NewReader(tt.key), []byte(tt.passphrase))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Fatalf("err = %v, want %v", err, tt.wantIs)
			}

			if err != nil {
				return
			}

			_, err = s.DetachedSign(strings.NewReader("data"))
			if (err == nil) != tt.wantSign {
				t.Errorf("DetachedSign err = %v, want success %v", err, tt.wantSign)
			}

			if _, err = s.CleartextSign(strings.NewReader("data")); err != nil {
				t.Errorf("CleartextSign: %v", err)
			}

			if _, err = s.SigningKey(); err != nil {
				t.Errorf("SigningKey: %v", err)
			}
		})
	}
}

func mustSigner(t *testing.T, e *openpgp.Entity) *LocalSigner {
	t.Helper()

	s, err := NewLocalSignerFromEntity(e)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func TestNewLocalSignerFromFile(t *testing.T) {

	dir := t.TempDir()
	good := filepath.Join(dir, "key.asc")

	if err := os.WriteFile(good, []byte(armoredPrivate(t, newEntity(t), "")), 0o600); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(dir, "bad.asc")

	if err := os.WriteFile(bad, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := NewLocalSignerFromFile(good, nil)
	if err != nil || s.KeyFingerprint() == "" {
		t.Fatalf("valid file: %v", err)
	}

	if _, err := NewLocalSignerFromFile(bad, nil); err == nil {
		t.Error("garbage file accepted")
	}

	if _, err := NewLocalSignerFromFile(filepath.Join(dir, "missing.asc"), nil); err == nil {
		t.Error("missing file accepted")
	}
}

func TestParsePrivateKeyInputs(t *testing.T) {

	tests := []struct {
		name string
		in   string
		want error
	}{
		{name: "binary garbage", in: "\x00\x01 garbage", want: ErrInvalidKey},
		{name: "empty", in: "", want: ErrInvalidKey},
		{name: "plain armored private key", in: armoredPrivate(t, newEntity(t), "")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			_, err := ParsePrivateKey(strings.NewReader(tt.in), nil)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSignerWithoutEntity(t *testing.T) {

	s := &LocalSigner{}

	if s.KeyFingerprint() != "" || s.KeyID() != "" {
		t.Error("empty signer reports a fingerprint")
	}

	if _, err := s.SigningKey(); !errors.Is(err, ErrNoKeysFound) {
		t.Errorf("SigningKey err = %v, want ErrNoKeysFound", err)
	}
}

func TestNewLocalSignerFromEntityRejects(t *testing.T) {

	cannotSign := newEntity(t)
	cannotSign.PrimaryKey.PubKeyAlgo = packet.PubKeyAlgoElGamal

	locked := newEntity(t)
	if err := locked.PrivateKey.Encrypt([]byte("pw")); err != nil {
		t.Fatal(err)
	}

	publicOnly := newEntity(t)
	publicOnly.PrivateKey = nil

	tests := []struct {
		name   string
		entity *openpgp.Entity
		want   error
	}{
		{"nil entity", nil, ErrNotPrivateKey},
		{"public only", publicOnly, ErrNotPrivateKey},
		{"locked private key", locked, ErrNotPrivateKey},
		{"algorithm cannot sign", cannotSign, ErrKeyCannotSign},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			if _, err := NewLocalSignerFromEntity(tt.entity); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestExportedPrivateKeysReload(t *testing.T) {

	orig := mustSigner(t, newEntity(t))

	exports := map[string]func() ([]byte, error){
		"ExportPrivateKey":            orig.ExportPrivateKey,
		"ExportUnprotectedPrivateKey": orig.ExportUnprotectedPrivateKey,
	}

	for name, export := range exports {
		t.Run(name, func(t *testing.T) {

			armored, err := export()
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.HasPrefix(armored, []byte("-----BEGIN PGP PRIVATE KEY BLOCK-----")) {
				t.Fatalf("not an armored private key: %.40s", armored)
			}

			back, err := ParsePrivateKey(bytes.NewReader(armored), nil)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}

			if back.KeyFingerprint() != orig.KeyFingerprint() {
				t.Errorf("fingerprint %s != %s", back.KeyFingerprint(), orig.KeyFingerprint())
			}
		})
	}
}

func TestCleartextSignReadFailure(t *testing.T) {

	s := mustSigner(t, newEntity(t))

	if _, err := s.CleartextSign(failingReader{}); err == nil {
		t.Error("read error swallowed")
	}

	if _, err := s.DetachedSign(failingReader{}); err == nil {
		t.Error("read error swallowed by DetachedSign")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestAlgorithmNames(t *testing.T) {

	ecdsa, err := openpgp.NewEntity("e", "", "e@example.com", &packet.Config{Algorithm: packet.PubKeyAlgoECDSA, Curve: packet.CurveNistP256})
	if err != nil {
		t.Fatal(err)
	}

	unknown := newEntity(t)
	unknown.PrimaryKey.PubKeyAlgo = packet.PublicKeyAlgorithm(99)

	tests := []struct {
		name string
		pk   *packet.PublicKey
		want string
	}{
		{"ecdsa", ecdsa.PrimaryKey, "ecdsa"},
		{"ed25519", newEntity(t).PrimaryKey, "ed25519"},
		{"unknown", unknown.PrimaryKey, "algo99"},
	}

	for _, tt := range tests {
		if got := algorithmName(tt.pk); got != tt.want {
			t.Errorf("%s: algorithmName = %q, want %q", tt.name, got, tt.want)
		}
	}
}
