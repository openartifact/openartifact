// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
)

func ungzip(t *testing.T, data []byte) []byte {
	t.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// debBytes builds a minimal .deb: an ar archive with debian-binary, a gzip
// control archive and an empty data archive.
func debBytes(t *testing.T, control string) []byte {
	t.Helper()

	member := func(name string, data []byte) []byte {
		var b bytes.Buffer

		hdr := []byte(strings.Repeat(" ", 60))
		copy(hdr[0:], name+"/")
		copy(hdr[16:], "0")
		copy(hdr[28:], "0")
		copy(hdr[34:], "0")
		copy(hdr[40:], "100644")
		copy(hdr[48:], itoa(len(data)))
		copy(hdr[58:], "`\n")
		b.Write(hdr)
		b.Write(data)

		if len(data)%2 == 1 {
			b.WriteByte('\n')
		}

		return b.Bytes()
	}

	var out bytes.Buffer

	out.WriteString("!<arch>\n")
	out.Write(member("debian-binary", []byte("2.0\n")))
	out.Write(member("control.tar.gz", archivetest.TarGz(t, archivetest.File("./control", control))))
	out.Write(member("data.tar.gz", archivetest.TarGz(t)))

	return out.Bytes()
}
