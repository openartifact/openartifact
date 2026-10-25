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

// FuzzParseDebFileAt feeds arbitrary bytes to the .deb parser.
func FuzzParseDebFileAt(f *testing.F) {
	f.Add([]byte("!<arch>\n"))
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ParseDebFileAt(bytes.NewReader(data), int64(len(data)))
	})
}
