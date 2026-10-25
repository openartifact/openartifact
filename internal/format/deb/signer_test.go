// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bytes"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/openartifact/openartifact/pkg/gpg"
)

func TestReleaseSigner_SignRelease(t *testing.T) {
	entity, err := gpg.GenerateEntity("Test Key", "", "test@example.com", gpg.AlgorithmRSA4096)
	if err != nil {
		t.Fatalf("GenerateEntity failed: %v", err)
	}

	var keyBuf bytes.Buffer

	w, _ := armor.Encode(&keyBuf, openpgp.PrivateKeyType, nil)
	_ = entity.SerializePrivate(w, nil)
	_ = w.Close()

	gpgSigner, err := gpg.NewLocalSigner(bytes.NewReader(keyBuf.Bytes()), nil)
	if err != nil {
		t.Fatalf("NewLocalSigner failed: %v", err)
	}

	signer := NewReleaseSigner(gpgSigner)

	releaseData := []byte("Origin: Test\nSuite: stable\n")

	signed, err := signer.SignRelease(releaseData)
	if err != nil {
		t.Fatalf("SignRelease failed: %v", err)
	}

	if !bytes.Equal(signed.Release, releaseData) {
		t.Error("Release data was modified")
	}

	if len(signed.InRelease) == 0 {
		t.Error("InRelease is empty")
	}

	if len(signed.ReleaseGPG) == 0 {
		t.Error("ReleaseGPG is empty")
	}

	if signed.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}
}
