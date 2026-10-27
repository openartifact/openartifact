// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestNewSigner_keyName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"openartifact-main.rsa.pub", true},
		{"alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub", true},
		{"a.rsa.pub", true},
		{"my_key+1.x.rsa.pub", true},
		{strings.Repeat("k", 87-len(".rsa.pub")) + ".rsa.pub", true},

		{strings.Repeat("k", 88-len(".rsa.pub")) + ".rsa.pub", false},
		{"", false},
		{".rsa.pub", false},
		{"key", false},
		{"key.pub", false},
		{"key.rsa", false},
		{"key.rsa.pub.bak", false},
		{"../key.rsa.pub", false},
		{"a/b.rsa.pub", false},
		{"a b.rsa.pub", false},
		{"a\n.rsa.pub", false},
		{"a\x00.rsa.pub", false},
		{"é.rsa.pub", false},
		{"a:b.rsa.pub", false},
	}

	key := testKey(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewSigner(tt.name, key)
			if (err == nil) != tt.want {
				t.Fatalf("NewSigner(%q): %v, want ok=%v", tt.name, err, tt.want)
			}

			if err == nil && s.KeyName() != tt.name {
				t.Fatalf("KeyName %q", s.KeyName())
			}
		})
	}
}

func TestNewSigner_key(t *testing.T) {
	good := testKey(t)

	small := *good
	small.N = new(big.Int).Rsh(good.N, 1)

	large := *good
	large.N = new(big.Int).Lsh(good.N, 14000)

	broken := *good
	broken.D = big.NewInt(3)

	tests := []struct {
		name string
		key  *rsa.PrivateKey
	}{
		{"nil", nil},
		{"no modulus", &rsa.PrivateKey{}},
		{"2047 bits", &small},
		{"more than 16384 bits", &large},
		{"inconsistent key", &broken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSigner("k.rsa.pub", tt.key); !errors.Is(err, errKey) {
				t.Fatalf("got %v, want errKey", err)
			}
		})
	}
}

func TestSigner_publicKey(t *testing.T) {
	s := testSigner(t)

	pemBytes, err := s.PublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}

	block, rest := pem.Decode(pemBytes)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		t.Fatalf("not one PUBLIC KEY block: %q", pemBytes)
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok || !rsaPub.Equal(&testKey(t).PublicKey) {
		t.Fatalf("PEM holds %T that is not the signing key", pub)
	}

	sum := sha256.Sum256(block.Bytes)
	if got, want := s.Fingerprint(), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("Fingerprint %s, want %s", got, want)
	}

	if len(s.Fingerprint()) != 64 {
		t.Fatalf("Fingerprint %q is not 64 hex digits", s.Fingerprint())
	}
}

func TestGenerateKey(t *testing.T) {
	for _, bits := range []int{-1, 0, 1024, 2047, 16385, 1 << 20} {
		if k, err := GenerateKey(bits); !errors.Is(err, errKey) || k != nil {
			t.Errorf("GenerateKey(%d) = %v, %v", bits, k, err)
		}
	}

	if got := testKey(t).N.BitLen(); got != 2048 {
		t.Fatalf("GenerateKey(2048) made a %d-bit key", got)
	}
}

func TestPrivateKeyPEM_roundTrip(t *testing.T) {
	key := testKey(t)

	t.Run("PKCS#8", func(t *testing.T) {
		pemBytes := MarshalPrivateKeyPEM(key)
		if block, _ := pem.Decode(pemBytes); block == nil || block.Type != "PRIVATE KEY" {
			t.Fatalf("not a PRIVATE KEY block: %q", pemBytes)
		}

		got, err := ParsePrivateKeyPEM(pemBytes)
		if err != nil {
			t.Fatal(err)
		}

		if !got.Equal(key) {
			t.Fatal("parsed key differs")
		}
	})

	t.Run("PKCS#1 as abuild-keygen writes it", func(t *testing.T) {
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

		got, err := ParsePrivateKeyPEM(pemBytes)
		if err != nil {
			t.Fatal(err)
		}

		if !got.Equal(key) {
			t.Fatal("parsed key differs")
		}
	})

	t.Run("leading text and a second block are ignored", func(t *testing.T) {
		pemBytes := append([]byte("# my key\n"), MarshalPrivateKeyPEM(key)...)
		pemBytes = append(pemBytes, []byte("-----BEGIN JUNK-----\nAAAA\n-----END JUNK-----\n")...)

		if _, err := ParsePrivateKeyPEM(pemBytes); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("nil key", func(t *testing.T) {
		if got := MarshalPrivateKeyPEM(nil); got != nil {
			t.Fatalf("got %q", got)
		}
	})
}

func TestParsePrivateKeyPEM_invalid(t *testing.T) {
	key := testKey(t)

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}

	ecSEC1, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}

	pkcs1 := x509.MarshalPKCS1PrivateKey(key)

	tests := []struct {
		name string
		pem  []byte
	}{
		{"empty", nil},
		{"not PEM", []byte("hello")},
		{"public key", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: key.N.Bytes()})},
		{"EC key in PKCS#8", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER})},
		{"EC key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecSEC1})},
		{"garbage DER in PKCS#8", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")})},
		{"garbage DER in PKCS#1", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("junk")})},
		{"truncated PKCS#1", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1[:len(pkcs1)/2]})},
		{"encrypted", pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: pkcs1,
			Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00112233445566778899AABBCCDDEEFF"},
		})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePrivateKeyPEM(tt.pem)
			if !errors.Is(err, errKey) || got != nil {
				t.Fatalf("got %v, %v, want errKey", got, err)
			}
		})
	}
}

func TestParsePrivateKeyPEM_weakKey(t *testing.T) {
	// Go refuses to generate 1024-bit keys unless asked to.
	t.Setenv("GODEBUG", "rsa1024min=0")

	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Skipf("cannot make a 1024-bit key: %v", err)
	}

	if _, err := ParsePrivateKeyPEM(MarshalPrivateKeyPEM(weak)); !errors.Is(err, errKey) {
		t.Fatalf("1024-bit key accepted: %v", err)
	}

	if _, err := NewSigner("weak.rsa.pub", weak); !errors.Is(err, errKey) {
		t.Fatalf("1024-bit signer accepted: %v", err)
	}
}
