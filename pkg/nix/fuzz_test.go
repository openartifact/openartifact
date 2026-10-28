// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"testing"
)

func FuzzParseNarInfo(f *testing.F) {
	f.Add([]byte(narInfoText()))
	f.Add([]byte(narInfoText("CA: fixed:r:sha256:abc")))
	f.Add([]byte("StorePath: " + testStorePath + "\nURL: nar/x.nar\nNarHash: sha256:" + emptySHA256Base32 + "\nNarSize: 1\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := ParseNarInfo(data)
		if err != nil {
			return
		}

		text := n.String()

		again, err := ParseNarInfo([]byte(text))
		if err != nil {
			t.Fatalf("rendered narinfo does not parse: %v\n%s", err, text)
		}

		if !sameNarInfo(n, again) {
			t.Fatalf("round trip changed the narinfo:\n%+v\n%+v", n, again)
		}

		if _, _, err := n.NarFile(); err == nil {
			if _, _, err := again.NarFile(); err != nil {
				t.Fatalf("NarFile accepted then rejected: %v", err)
			}
		}

		if _, err := n.Fingerprint(); err != nil {
			t.Fatalf("Fingerprint of a parsed narinfo: %v", err)
		}
	})
}

func FuzzDecodeBase32(f *testing.F) {
	f.Add(emptySHA256Base32, 32)
	f.Add(testHash, 20)
	f.Add("", 0)

	f.Fuzz(func(t *testing.T, s string, size int) {
		if size < 0 || size > 1<<12 {
			return
		}

		b, err := DecodeBase32(s, size)
		if err != nil {
			return
		}

		if got := EncodeBase32(b); got != s {
			t.Fatalf("decode/encode changed %q to %q", s, got)
		}
	})
}

func FuzzParseNarFile(f *testing.F) {
	f.Add(emptySHA256Base32 + ".nar.xz")
	f.Add(emptySHA256Base32 + ".nar")
	f.Add("../" + emptySHA256Base32 + ".nar.zst")

	f.Fuzz(func(t *testing.T, file string) {
		hash, err := ParseNarFile(file)
		if err != nil {
			return
		}

		if len(file) < len(hash.Base32()) || file[:len(hash.Base32())] != hash.Base32() {
			t.Fatalf("file %q does not start with its hash %s", file, hash.Base32())
		}
	})
}

func FuzzParseSHA256(f *testing.F) {
	f.Add("sha256:" + emptySHA256Base32)
	f.Add("sha256:" + testFileHash().Hex())
	f.Add("sha256-" + b64(testFileHash()))

	f.Fuzz(func(t *testing.T, s string) {
		h, err := ParseSHA256(s)
		if err != nil {
			return
		}

		again, err := ParseSHA256(h.String())
		if err != nil || again != h {
			t.Fatalf("canonical form of %q does not parse back: %v", s, err)
		}
	})
}

func FuzzSplitStorePath(f *testing.F) {
	f.Add(testStorePath)
	f.Add(StoreDir + "/../etc/passwd")
	f.Add(testHash + "-name")

	f.Fuzz(func(t *testing.T, path string) {
		hash, name, err := SplitStorePath(path)
		if err != nil {
			return
		}

		if want := StoreDir + "/" + hash + "-" + name; path != want {
			t.Fatalf("SplitStorePath(%q) = %q, %q, which rebuilds as %q", path, hash, name, want)
		}

		if !ValidStoreHash(hash) || !ValidName(name) {
			t.Fatalf("SplitStorePath(%q) returned invalid parts %q, %q", path, hash, name)
		}
	})
}
