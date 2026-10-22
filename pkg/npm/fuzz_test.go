// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

func fuzzTarball(name, content string) []byte {
	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))})
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// FuzzParseTarball feeds arbitrary bytes to the npm tarball parser.
func FuzzParseTarball(f *testing.F) {
	f.Add(fuzzTarball("package/package.json", `{"name":"demo","version":"1.0.0"}`))
	f.Add(fuzzTarball("package/package.json", "{"))
	f.Add(fuzzTarball("evil/../package.json", "{}"))
	f.Add([]byte{0x1f, 0x8b})
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _, _, _ = ParseTarball(bytes.NewReader(data))
	})
}

// FuzzValidatePackageName feeds arbitrary package names and versions.
func FuzzValidatePackageName(f *testing.F) {
	f.Add("@scope/name", "1.2.3-beta.1+build")
	f.Add("../../etc", "1.x")
	f.Add("", "")

	f.Fuzz(func(_ *testing.T, name, version string) {
		_ = ValidatePackageName(name)
		_ = ValidateSemver(version)
	})
}
