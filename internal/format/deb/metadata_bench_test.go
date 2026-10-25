// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

//nolint:gochecknoglobals // benchmark sizes
var benchEntryCounts = []int{100, 1000, 10000}

func benchDebEntries(n int) []models.DebPackageEntry {
	entries := make([]models.DebPackageEntry, n)

	for i := range entries {
		name := "libexample" + strconv.Itoa(i)

		entries[i] = models.DebPackageEntry{
			ID:             strconv.Itoa(i),
			Name:           name,
			Version:        "1:2." + strconv.Itoa(i%40) + ".0-1~bpo12+1",
			Arch:           "amd64",
			Filename:       name + "_2.0.0-1_amd64.deb",
			Distribution:   "bookworm",
			DistributionID: "dist-1",
			ComponentName:  "main",
			SHA256:         fmt.Sprintf("%064x", i),
			Size:           123456 + int64(i),
			InstalledSize:  1234,
			ControlFields: map[string]string{
				"Maintainer":  "Jane Maintainer <jane@example.org>",
				"Depends":     "libc6 (>= 2.34), libexample1 (= 1:2.4.6-1~bpo12+1), zlib1g-dev | libz-dev",
				"Recommends":  "pkg-config",
				"Section":     "libdevel",
				"Priority":    "optional",
				"Multi-Arch":  "same",
				"Homepage":    "https://example.org/" + name,
				"X-Custom":    "custom value",
				"Description": "development files for " + name + "\n This package contains the headers and\n static libraries.",
			},
		}
	}

	return entries
}

func benchPackagesGenerator(n int) *PackagesFileGenerator {
	gen := NewPackagesFileGenerator()

	for _, e := range benchDebEntries(n) {
		gen.AddEntry(FormatPackagesEntry(e))
	}

	return gen
}

func BenchmarkFormatPackagesEntry(b *testing.B) {
	e := benchDebEntries(1)[0]

	b.ReportAllocs()

	for b.Loop() {
		_ = FormatPackagesEntry(e)
	}
}

// BenchmarkPackagesFile renders n stanzas and joins them (what the worker does
// per component/architecture).
func BenchmarkPackagesFile(b *testing.B) {
	for _, n := range benchEntryCounts {
		entries := benchDebEntries(n)

		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			var size int

			for b.Loop() {
				gen := NewPackagesFileGenerator()
				for _, e := range entries {
					gen.AddEntry(FormatPackagesEntry(e))
				}

				size = len(gen.Generate())
			}

			b.SetBytes(int64(size))
		})
	}
}

func BenchmarkPackagesGenerateGzipped(b *testing.B) {
	for _, n := range benchEntryCounts {
		gen := benchPackagesGenerator(n)

		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(gen.Generate())))

			for b.Loop() {
				if _, err := gen.GenerateGzipped(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPackagesGenerateXZipped(b *testing.B) {
	for _, n := range benchEntryCounts {
		gen := benchPackagesGenerator(n)

		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(gen.Generate())))

			for b.Loop() {
				if _, err := gen.GenerateXZipped(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGenerateRelease(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		files := make([]FileInfo, n)
		for i := range files {
			files[i] = FileInfo{
				Filename: fmt.Sprintf("main/binary-amd64/Packages-%d.gz", i),
				Size:     int64(100000 + i),
				MD5sum:   fmt.Sprintf("%032x", i),
				SHA1:     fmt.Sprintf("%040x", i),
				SHA256:   fmt.Sprintf("%064x", i),
			}
		}

		rfg := NewReleaseFileGenerator("openartifact", "openartifact", "bookworm", "bookworm")
		rfg.AddComponent("main")
		rfg.AddComponent("contrib")
		rfg.AddArchitecture("amd64")
		rfg.AddArchitecture("arm64")

		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				_ = rfg.GenerateRelease(files)
			}
		})
	}
}

func BenchmarkCalculateChecksums(b *testing.B) {
	for _, size := range []int{4 << 10, 1 << 20, 16 << 20} {
		data := make([]byte, size)

		b.Run(fmt.Sprintf("size=%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(size))

			for b.Loop() {
				_, _, _ = CalculateChecksums(data)
			}
		})
	}
}
