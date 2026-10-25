// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpg

import (
	"bytes"
	"io"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

func TestLocalSigner(t *testing.T) {
	entity, err := GenerateEntity("Test User", "", "test@example.com", AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity failed: %v", err)
	}

	var keyBuf bytes.Buffer

	w, err := armor.Encode(&keyBuf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatalf("Failed to create armor encoder: %v", err)
	}

	err = entity.SerializePrivate(w, nil)
	if err != nil {
		t.Fatalf("Failed to serialize private key: %v", err)
	}

	_ = w.Close()

	signer, err := NewLocalSigner(bytes.NewReader(keyBuf.Bytes()), nil)
	if err != nil {
		t.Fatalf("NewLocalSigner failed: %v", err)
	}

	if signer.KeyFingerprint() == "" {
		t.Fatal("KeyFingerprint returned empty string")
	}

	if signer.KeyID() == "" {
		t.Fatal("KeyID returned empty string")
	}
}

func TestDetachedSign(t *testing.T) {
	entity, err := GenerateEntity("Test User", "", "test@example.com", AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity failed: %v", err)
	}

	var keyBuf bytes.Buffer

	w, err := armor.Encode(&keyBuf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatalf("Failed to create armor encoder: %v", err)
	}

	err = entity.SerializePrivate(w, nil)
	if err != nil {
		t.Fatalf("Failed to serialize private key: %v", err)
	}

	_ = w.Close()

	signer, err := NewLocalSigner(bytes.NewReader(keyBuf.Bytes()), nil)
	if err != nil {
		t.Fatalf("NewLocalSigner failed: %v", err)
	}

	data := bytes.NewReader([]byte("test data to sign"))

	sig, err := signer.DetachedSign(data)
	if err != nil {
		t.Fatalf("DetachedSign failed: %v", err)
	}

	if len(sig) == 0 {
		t.Fatal("DetachedSign returned empty signature")
	}

	if !bytes.Contains(sig, []byte("-----BEGIN PGP SIGNATURE-----")) {
		t.Fatal("Signature missing PGP armor header")
	}
}

func TestPublicKey(t *testing.T) {
	entity, err := GenerateEntity("Test User", "", "test@example.com", AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity failed: %v", err)
	}

	var keyBuf bytes.Buffer

	w, err := armor.Encode(&keyBuf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatalf("Failed to create armor encoder: %v", err)
	}

	err = entity.SerializePrivate(w, nil)
	if err != nil {
		t.Fatalf("Failed to serialize private key: %v", err)
	}

	_ = w.Close()

	signer, err := NewLocalSigner(bytes.NewReader(keyBuf.Bytes()), nil)
	if err != nil {
		t.Fatalf("NewLocalSigner failed: %v", err)
	}

	pubKey, err := signer.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey failed: %v", err)
	}

	if len(pubKey) == 0 {
		t.Fatal("PublicKey returned empty key")
	}

	if !bytes.Contains(pubKey, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		t.Fatal("Public key missing PGP armor header")
	}
}

func TestCleartextSign(t *testing.T) {
	entity, err := GenerateEntity("Test User", "", "test@example.com", AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity failed: %v", err)
	}

	var keyBuf bytes.Buffer

	w, err := armor.Encode(&keyBuf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatalf("Failed to create armor encoder: %v", err)
	}

	err = entity.SerializePrivate(w, nil)
	if err != nil {
		t.Fatalf("Failed to serialize private key: %v", err)
	}

	_ = w.Close()

	signer, err := NewLocalSigner(bytes.NewReader(keyBuf.Bytes()), nil)
	if err != nil {
		t.Fatalf("NewLocalSigner failed: %v", err)
	}

	data := bytes.NewReader([]byte("test data to sign"))

	signed, err := signer.CleartextSign(data)
	if err != nil {
		t.Fatalf("CleartextSign failed: %v", err)
	}

	signedData, err := io.ReadAll(signed)
	if err != nil {
		t.Fatalf("Failed to read signed data: %v", err)
	}

	if len(signedData) == 0 {
		t.Fatal("CleartextSign returned empty data")
	}

	if !bytes.Contains(signedData, []byte("-----BEGIN PGP SIGNED MESSAGE-----")) {
		t.Fatal("Signed message missing cleartext header")
	}

	if !bytes.Contains(signedData, []byte("-----BEGIN PGP SIGNATURE-----")) {
		t.Fatal("Signed message missing signature block")
	}

	if !bytes.Contains(signedData, []byte("test data to sign")) {
		t.Fatal("Signed message missing original content")
	}
}

func TestExportPrivateKey(t *testing.T) {
	entity, err := GenerateEntity("Test User", "", "test@example.com", AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity failed: %v", err)
	}

	var keyBuf bytes.Buffer

	w, err := armor.Encode(&keyBuf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatalf("Failed to create armor encoder: %v", err)
	}

	err = entity.SerializePrivate(w, nil)
	if err != nil {
		t.Fatalf("Failed to serialize private key: %v", err)
	}

	_ = w.Close()

	signer, err := NewLocalSigner(bytes.NewReader(keyBuf.Bytes()), nil)
	if err != nil {
		t.Fatalf("NewLocalSigner failed: %v", err)
	}

	exported, err := signer.ExportPrivateKey()
	if err != nil {
		t.Fatalf("ExportPrivateKey failed: %v", err)
	}

	if len(exported) == 0 {
		t.Fatal("ExportPrivateKey returned empty key")
	}

	if !bytes.Contains(exported, []byte("-----BEGIN PGP PRIVATE KEY BLOCK-----")) {
		t.Fatal("Exported key missing PGP armor header")
	}
}

func TestCleartextSignVerifiesAsTextSignature(t *testing.T) {
	entity, err := GenerateEntity("Test User", "", "test@example.com", AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity failed: %v", err)
	}

	signer := &LocalSigner{entity: entity}

	r, err := signer.CleartextSign(bytes.NewReader([]byte("Origin: x\n-dash line\nSuite: y\n")))
	if err != nil {
		t.Fatalf("CleartextSign failed: %v", err)
	}

	out, _ := io.ReadAll(r)

	block, _ := clearsign.Decode(out)
	if block == nil {
		t.Fatalf("not a valid clearsigned message: %q", out)
	}

	if !bytes.Contains(out, []byte("- -dash line")) {
		t.Error("lines starting with '-' must be dash-escaped")
	}

	if string(block.Bytes) != "Origin: x\r\n-dash line\r\nSuite: y\r\n" {
		t.Errorf("unexpected plaintext %q", block.Bytes)
	}

	_, err = openpgp.CheckDetachedSignature(openpgp.EntityList{entity}, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil)
	if err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}
