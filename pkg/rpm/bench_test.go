// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"os"
	"testing"
)

func benchRPMFixture(b *testing.B) []byte {
	b.Helper()

	data, err := os.ReadFile("testdata/weakdeps.rpm")
	if err != nil {
		b.Fatal(err)
	}

	return data
}

// BenchmarkParseRPM measures lead + signature header + package header parsing
// and Package construction from an in-memory RPM.
func BenchmarkParseRPM(b *testing.B) {
	data := benchRPMFixture(b)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()

	for b.Loop() {
		if _, err := ParseRPM(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseHeader isolates the raw header parse (index entries + store).
func BenchmarkParseHeader(b *testing.B) {
	data := benchRPMFixture(b)

	r := bytes.NewReader(data)
	if _, err := ParseLead(r); err != nil {
		b.Fatal(err)
	}

	hdrStart := len(data) - r.Len()
	hdrBytes := data[hdrStart:]

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := ParseHeader(bytes.NewReader(hdrBytes), 1<<24); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPackageExtra measures the persisted-extra round trip done for every
// package on upload and on every metadata regeneration.
func BenchmarkPackageExtra(b *testing.B) {
	pkg, err := ParseRPM(bytes.NewReader(benchRPMFixture(b)))
	if err != nil {
		b.Fatal(err)
	}

	extra, err := pkg.MarshalExtra()
	if err != nil {
		b.Fatal(err)
	}

	b.Run("Marshal", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := pkg.MarshalExtra(); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Unmarshal", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			var p Package
			if err := p.UnmarshalExtra(extra); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("GetMetadata", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			_ = pkg.GetMetadata()
		}
	})
}
