// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzReadPackage feeds arbitrary bytes to the package reader.
func FuzzReadPackage(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x00})
	f.Add([]byte{0x1f, 0x8b, 0x08, 0x00})
	f.Add([]byte{0xfd, '7', 'z', 'X', 'Z', 0x00})

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ReadPackage(bytes.NewReader(data))
	})
}

// FuzzParsePkgInfo feeds arbitrary text to the .PKGINFO parser.
func FuzzParsePkgInfo(f *testing.F) {
	f.Add(samplePkgInfo)
	f.Add("pkgname = a\npkgver = 1-1\narch = any\n")
	f.Add("pkgname=a\npkgver=1:2-3\narch=x86_64\nsize=99999999999999999999\n")
	f.Add("")

	f.Fuzz(func(t *testing.T, in string) {
		info, err := ParsePkgInfo(strings.NewReader(in))
		if err != nil {
			return
		}

		if !ValidName(info.Name) || !ValidVersion(info.Version) || !ValidArch(info.Arch) {
			t.Fatalf("accepted an invalid identity: %+v", info)
		}
	})
}

// FuzzVercmp checks that the comparison is antisymmetric.
func FuzzVercmp(f *testing.F) {
	f.Add("1.0-1", "1.0-2")
	f.Add("1:1.0", "2.0")
	f.Add("1.0a", "1.0")
	f.Add("", "x")

	f.Fuzz(func(t *testing.T, a, b string) {
		if Vercmp(a, b) != -Vercmp(b, a) {
			t.Fatalf("Vercmp(%q, %q) is not antisymmetric", a, b)
		}
	})
}

// FuzzNormalizeSignature feeds arbitrary bytes to the signature check.
func FuzzNormalizeSignature(f *testing.F) {
	f.Add([]byte("-----BEGIN PGP SIGNATURE-----\n\niQEzBAABCAAdFiEE\n-----END PGP SIGNATURE-----\n"))
	f.Add([]byte{0x89, 0x01, 0x00})
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = NormalizeSignature(data)
	})
}
