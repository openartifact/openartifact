// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"bytes"
	"testing"
)

func TestComputeChecksums(t *testing.T) {
	md5Hex, sha1Hex, sha256Hex, sha512Hex, err := ComputeChecksums(bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if md5Hex != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("MD5 = %q, want %q", md5Hex, "5d41402abc4b2a76b9719d911017c592")
	}

	if sha1Hex != "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d" {
		t.Errorf("SHA1 = %q, want %q", sha1Hex, "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d")
	}

	if sha256Hex != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("SHA256 = %q, want %q", sha256Hex, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824")
	}

	if sha512Hex != "9b71d224bd62f3785d96d46ad3ea3d73319bfbc2890caadae2dff72519673ca72323c3d99ba5c11d7c7acc6e14b8c5da0c4663475c2e5c3adef46f73bcdec043" {
		t.Errorf(
			"SHA512 = %q, want %q",
			sha512Hex,
			"9b71d224bd62f3785d96d46ad3ea3d73319bfbc2890caadae2dff72519673ca72323c3d99ba5c11d7c7acc6e14b8c5da0c4663475c2e5c3adef46f73bcdec043",
		)
	}
}
