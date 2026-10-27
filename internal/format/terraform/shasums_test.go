// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"bytes"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

func TestBuildSHA256SUMS(t *testing.T) {
	in := []sumEntry{
		{sha256: "bb", filename: "terraform-provider-x_1.0.0_linux_arm64.zip"},
		{sha256: "aa", filename: "terraform-provider-x_1.0.0_darwin_amd64.zip"},
	}

	want := "aa  terraform-provider-x_1.0.0_darwin_amd64.zip\nbb  terraform-provider-x_1.0.0_linux_arm64.zip\n"
	if got := string(buildSHA256SUMS(in)); got != want {
		t.Fatalf("buildSHA256SUMS =\n%s\nwant\n%s", got, want)
	}

	if in[0].sha256 != "bb" {
		t.Fatal("buildSHA256SUMS reordered its input")
	}

	if len(buildSHA256SUMS(nil)) != 0 {
		t.Fatal("no entries must give an empty file")
	}
}

// TestBinarySignature: the signature the repository's signer produces is armored;
// the .sig file Terraform downloads must be the binary form, and it must verify
// the checksum file against the signer's public key.
func TestBinarySignature(t *testing.T) {
	signer := newSigner(t)
	sums := buildSHA256SUMS([]sumEntry{{sha256: "aa", filename: "f.zip"}})

	armored, err := signer.DetachedSign(bytes.NewReader(sums))
	if err != nil {
		t.Fatal(err)
	}

	sig, err := binarySignature(armored)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(sig, []byte("BEGIN PGP")) {
		t.Fatal("signature is still armored")
	}

	pub, err := signer.PublicKey()
	if err != nil {
		t.Fatal(err)
	}

	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(pub))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(sums), bytes.NewReader(sig), nil); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}

	if _, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(append(sums, 'x')), bytes.NewReader(sig), nil); err == nil {
		t.Fatal("signature verified a different checksum file")
	}
}

func TestBinarySignatureRejects(t *testing.T) {
	for name, in := range map[string]string{
		"not armor":      "hello",
		"empty":          "",
		"wrong type":     "-----BEGIN PGP MESSAGE-----\n\naGk=\n=NoQs\n-----END PGP MESSAGE-----\n",
		"empty armor":    "-----BEGIN PGP SIGNATURE-----\n\n-----END PGP SIGNATURE-----\n",
		"truncated":      "-----BEGIN PGP SIGNATURE-----\n\naGk",
		"binary garbage": "\x00\x01\x02",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := binarySignature([]byte(in)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestKeyID(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789ABCDEF0123456789ABCDEF01234567": "89ABCDEF01234567",
		"0123456789ABCDEF":                         "0123456789ABCDEF",
		"ABCD":                                     "ABCD",
		"":                                         "",
	} {
		if got := keyID(in); got != want {
			t.Errorf("keyID(%q) = %q, want %q", in, got, want)
		}
	}
}
