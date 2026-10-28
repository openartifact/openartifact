// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"os"
	"testing"
)

// cacheNixosOrgKey is the public key clients trust for the public binary cache.
const cacheNixosOrgKey = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="

// TestVerifyRealNarInfo is a known-answer test: a narinfo as the public binary
// cache serves it, whose signature a Nix client verifies with the key above.
func TestVerifyRealNarInfo(t *testing.T) {
	data, err := os.ReadFile("testdata/cache_nixos_org_hello.narinfo")
	if err != nil {
		t.Fatal(err)
	}

	n, err := ParseNarInfo(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(n.Sigs) != 1 || !verifyNarInfo(t, cacheNixosOrgKey, n.Sigs[0], n) {
		t.Fatalf("signature %v does not verify", n.Sigs)
	}

	n.NarSize++

	if verifyNarInfo(t, cacheNixosOrgKey, n.Sigs[0], n) {
		t.Error("a changed NarSize still verifies")
	}

	// The URL of this cache names the NAR hash, not the file hash.
	file, hash, err := n.NarFile()
	if err != nil || file != "1ikjnm41hig7040f8bndzqjg1ldxmf536y6s780r51lcml7xcn56.nar.zst" || hash.Base32() != file[:52] {
		t.Errorf("NarFile = %q, %v, %v", file, hash, err)
	}
}
