// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bytes"
	"testing"
)

// FuzzParseControlFile feeds arbitrary text to the control-file parser.
func FuzzParseControlFile(f *testing.F) {
	f.Add("Package: test\nVersion: 1.0\nArchitecture: amd64\nDepends: libc6 (>= 2.3), foo | bar\nDescription: x\n y\n .\n z\n\n")
	f.Add("Package: a\n continuation without field\n")
	f.Add(": no name\n\n\n")
	f.Add("")

	f.Fuzz(func(_ *testing.T, in string) {
		_, _ = ParseControlFile(bytes.NewReader([]byte(in)))
	})
}

// FuzzParseDependencies feeds arbitrary relationship strings.
func FuzzParseDependencies(f *testing.F) {
	f.Add("libc6 (>= 2.3), foo | bar [amd64], baz:any")
	f.Add("a (<< 1), (")
	f.Add("")

	f.Fuzz(func(_ *testing.T, in string) {
		_, _ = ParseDependencies(in)
	})
}

// FuzzReadARArchive feeds arbitrary bytes to the ar container reader.
func FuzzReadARArchive(f *testing.F) {
	ar := append([]byte("!<arch>\n"), buildARMemberHeader("debian-binary", "4")...)
	ar = append(ar, []byte("2.0\n")...)

	f.Add(ar)
	f.Add([]byte("!<arch>\n"))
	f.Add(append([]byte("!<arch>\n"), buildARMemberHeader("x", "-1")...))
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ReadARArchive(bytes.NewReader(data), 1<<20)
	})
}

// FuzzParseDebFile feeds arbitrary bytes to the .deb parser.
func FuzzParseDebFile(f *testing.F) {
	f.Add([]byte("!<arch>\n"))
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ParseDebFile(bytes.NewReader(data))
	})
}
