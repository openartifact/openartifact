// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

const benchControl = `Package: libexample-dev
Version: 1:2.4.6-1~bpo12+1
Architecture: amd64
Maintainer: Jane Maintainer <jane@example.org>
Installed-Size: 1234
Depends: libc6 (>= 2.34), libexample1 (= 1:2.4.6-1~bpo12+1), zlib1g-dev | libz-dev, libssl-dev (>= 3.0)
Recommends: pkg-config
Suggests: libexample-doc
Section: libdevel
Priority: optional
Multi-Arch: same
Homepage: https://example.org/libexample
X-Custom-Field: custom value
Description: development files for libexample
 This package contains the headers and static libraries required to build
 software against libexample.
 .
 It also ships pkg-config metadata.
`

// benchDeb builds a minimal .deb (debian-binary, control.tar.gz, data.tar.gz)
// whose data archive carries dataSize bytes of payload.
func benchDeb(b *testing.B, dataSize int) []byte {
	b.Helper()

	tarGz := func(name string, content []byte) []byte {
		var buf bytes.Buffer

		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)

		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			b.Fatal(err)
		}

		if _, err := tw.Write(content); err != nil {
			b.Fatal(err)
		}

		if err := tw.Close(); err != nil {
			b.Fatal(err)
		}

		if err := gw.Close(); err != nil {
			b.Fatal(err)
		}

		return buf.Bytes()
	}

	member := func(out *bytes.Buffer, name string, data []byte) {
		fmt.Fprintf(out, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, 0, 0, 0, "100644", len(data))
		out.Write(data)

		if len(data)%2 != 0 {
			out.WriteByte('\n')
		}
	}

	// Incompressible payload so the data member really is dataSize bytes.
	payload := make([]byte, dataSize)
	rng := rand.New(rand.NewPCG(1, 2))

	for i := range payload {
		payload[i] = byte(rng.Uint32())
	}

	var out bytes.Buffer

	out.WriteString("!<arch>\n")
	member(&out, "debian-binary", []byte("2.0\n"))
	member(&out, "control.tar.gz", tarGz("./control", []byte(benchControl)))
	member(&out, "data.tar.gz", tarGz("./usr/lib/libexample.a", payload))

	return out.Bytes()
}

func BenchmarkParseControlFile(b *testing.B) {
	head, desc, _ := strings.Cut(benchControl, "Description:")

	for _, extra := range []int{0, 10, 100} {
		var sb strings.Builder

		sb.WriteString(head)

		for i := range extra {
			fmt.Fprintf(&sb, "X-Extra-%d: value number %d\n", i, i)
		}

		sb.WriteString("Description:" + desc)
		data := sb.String()

		b.Run(fmt.Sprintf("extra_fields=%d", extra), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))

			for b.Loop() {
				if _, err := ParseControlFile(strings.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkControlParsePackage(b *testing.B) {
	cf, err := ParseControlFile(strings.NewReader(benchControl))
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		if _, err := cf.ParsePackage(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseDebFile(b *testing.B) {
	for _, size := range []int{1 << 10, 1 << 20} {
		data := benchDeb(b, size)

		b.Run(fmt.Sprintf("payload=%dB", size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))

			for b.Loop() {
				if _, err := ParseDebFileAt(bytes.NewReader(data), int64(len(data))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
