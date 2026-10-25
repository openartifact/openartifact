// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"fmt"
	"io"
	"strconv"
	"testing"

	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

//nolint:gochecknoglobals // benchmark sizes
var benchPackageCounts = []int{100, 1000, 10000}

// benchRPMPackages builds n realistic packages: a dozen provides, ~25 requires
// (versioned, rpmlib, glibc symbols, script-time), 30 files and 5 changelogs.
func benchRPMPackages(n int) []pkgrpm.Package {
	pkgs := make([]pkgrpm.Package, n)

	for i := range pkgs {
		name := "libexample" + strconv.Itoa(i)
		ver := "1." + strconv.Itoa(i%50) + ".0"

		p := pkgrpm.Package{
			Name:          name,
			Version:       ver,
			Release:       "3.el9",
			Epoch:         uint32(i % 3),
			Arch:          "x86_64",
			Summary:       "Example library number " + strconv.Itoa(i),
			Description:   "Example library providing a fairly long description that mimics what real packages ship in their spec files.",
			License:       "MIT",
			URL:           "https://example.org/" + name,
			Vendor:        "Example Vendor",
			Group:         "System Environment/Libraries",
			Packager:      "Builder <builder@example.org>",
			SourceRPM:     name + "-" + ver + "-3.el9.src.rpm",
			BuildHost:     "builder01.example.org",
			BuildTime:     1_700_000_000 + int64(i),
			PackageSize:   123456 + int64(i),
			InstalledSize: 654321,
			ArchiveSize:   234567,
			SHA256:        fmt.Sprintf("%064x", i),
			HeaderStart:   280,
			HeaderEnd:     9000 + int64(i%100),
		}

		p.Provides = append(p.Provides,
			pkgrpm.Dependency{Name: name, Flags: pkgrpm.SENSE_EQUAL, Version: ver + "-3.el9"},
			pkgrpm.Dependency{Name: name + "(x86-64)", Flags: pkgrpm.SENSE_EQUAL, Version: ver + "-3.el9"},
			pkgrpm.Dependency{Name: "lib" + name + ".so.1()(64bit)"},
		)

		for j := range 9 {
			p.Provides = append(p.Provides, pkgrpm.Dependency{Name: fmt.Sprintf("%s-feature%d", name, j)})
		}

		p.Requires = append(p.Requires,
			pkgrpm.Dependency{Name: "rpmlib(CompressedFileNames)", Flags: pkgrpm.SENSE_LESSEQUAL | pkgrpm.SENSE_RPMLIB, Version: "3.0.4-1"},
			pkgrpm.Dependency{Name: "rpmlib(PayloadIsZstd)", Flags: pkgrpm.SENSE_LESSEQUAL | pkgrpm.SENSE_RPMLIB, Version: "5.4.18-1"},
			pkgrpm.Dependency{Name: "/bin/sh", Flags: pkgrpm.SENSE_SCRIPT_PRE},
			pkgrpm.Dependency{Name: "libc.so.6()(64bit)"},
			pkgrpm.Dependency{Name: "libc.so.6(GLIBC_2.2.5)(64bit)"},
			pkgrpm.Dependency{Name: "libc.so.6(GLIBC_2.14)(64bit)"},
			pkgrpm.Dependency{Name: "libc.so.6(GLIBC_2.34)(64bit)"},
			pkgrpm.Dependency{Name: "libm.so.6(GLIBC_2.29)(64bit)"},
			pkgrpm.Dependency{Name: "libz.so.1()(64bit)"},
			pkgrpm.Dependency{Name: "openssl-libs", Flags: pkgrpm.SENSE_GREATEREQUAL, Version: "3.0.0-1"},
			pkgrpm.Dependency{Name: name + "(x86-64)", Flags: pkgrpm.SENSE_EQUAL, Version: ver + "-3.el9"},
		)

		for j := range 14 {
			p.Requires = append(p.Requires, pkgrpm.Dependency{Name: fmt.Sprintf("dep%d", j), Flags: pkgrpm.SENSE_GREATEREQUAL, Version: "2." + strconv.Itoa(j)})
		}

		p.Recommends = []pkgrpm.Dependency{{Name: "recommended-tool"}}
		p.Suggests = []pkgrpm.Dependency{{Name: "suggested-docs", Flags: pkgrpm.SENSE_GREATEREQUAL, Version: "1.0"}}
		p.Conflicts = []pkgrpm.Dependency{{Name: name + "-legacy"}}
		p.Obsoletes = []pkgrpm.Dependency{{Name: name + "-old", Flags: pkgrpm.SENSE_LESS, Version: "0.9"}}

		for j := range 30 {
			p.Files = append(p.Files, pkgrpm.FileEntry{
				Path: fmt.Sprintf("/usr/lib64/%s/plugins/module%d.so", name, j),
				Size: 4096, Mode: 0o755,
			})
		}

		for j := range 5 {
			p.Changelogs = append(p.Changelogs, pkgrpm.ChangelogEntry{
				Time:        1_690_000_000 - int64(j)*86400,
				Author:      "Packager <packager@example.org> - 1." + strconv.Itoa(j) + ".0-1",
				Description: "- Update to upstream release; fix several issues reported downstream.",
			})
		}

		pkgs[i] = p
	}

	return pkgs
}

func benchEachCount(b *testing.B, fn func(b *testing.B, pkgs []pkgrpm.Package)) {
	b.Helper()

	for _, n := range benchPackageCounts {
		pkgs := benchRPMPackages(n)

		b.Run(fmt.Sprintf("packages=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			fn(b, pkgs)
		})
	}
}

func BenchmarkGeneratePrimaryXML(b *testing.B) {
	benchEachCount(b, func(b *testing.B, pkgs []pkgrpm.Package) {
		b.Helper()

		var size int

		for b.Loop() {
			out, err := renderXML(WritePrimaryXML, pkgs)
			if err != nil {
				b.Fatal(err)
			}

			size = len(out)
		}

		b.SetBytes(int64(size))
	})
}

func BenchmarkGenerateFilelistsXML(b *testing.B) {
	benchEachCount(b, func(b *testing.B, pkgs []pkgrpm.Package) {
		b.Helper()

		var size int

		for b.Loop() {
			out, err := renderXML(WriteFilelistsXML, pkgs)
			if err != nil {
				b.Fatal(err)
			}

			size = len(out)
		}

		b.SetBytes(int64(size))
	})
}

func BenchmarkGenerateOtherXML(b *testing.B) {
	benchEachCount(b, func(b *testing.B, pkgs []pkgrpm.Package) {
		b.Helper()

		var size int

		for b.Loop() {
			out, err := renderXML(WriteOtherXML, pkgs)
			if err != nil {
				b.Fatal(err)
			}

			size = len(out)
		}

		b.SetBytes(int64(size))
	})
}

// BenchmarkWritePrimaryXML streams primary.xml to io.Discard: the steady-state
// cost when the caller does not need the whole document in memory.
func BenchmarkWritePrimaryXML(b *testing.B) {
	benchEachCount(b, func(b *testing.B, pkgs []pkgrpm.Package) {
		b.Helper()

		var size int64

		for b.Loop() {
			cw := &countingWriter{w: io.Discard}
			if err := WritePrimaryXML(cw, pkgs); err != nil {
				b.Fatal(err)
			}

			size = cw.n
		}

		b.SetBytes(size)
	})
}

func BenchmarkFilterRequires(b *testing.B) {
	pkgs := benchRPMPackages(1)

	b.ReportAllocs()

	for b.Loop() {
		_ = filterRequires(&pkgs[0])
	}
}

func BenchmarkGenerateRepomdXML(b *testing.B) {
	entries := make([]RepomdData, 0, 7)

	for _, t := range []string{"primary", "filelists", "other", "primary_db", "filelists_db", "other_db", "group"} {
		entries = append(entries, RepomdData{
			Type: t, Location: "repodata/" + fmt.Sprintf("%064x", len(t)) + "-" + t + ".xml.gz",
			Checksum: fmt.Sprintf("%064x", len(t)), ChecksumType: "sha256",
			Timestamp: 1_700_000_000, Size: 123456, OpenSize: 654321,
		})
	}

	b.ReportAllocs()

	for b.Loop() {
		if _, err := GenerateRepomdXML("1700000000", entries); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCompressMetadata measures the gzip(best)+xz compression the worker
// runs for primary.xml on every regeneration. The 10000-package case is
// skipped under -short.
func BenchmarkCompressMetadata(b *testing.B) {
	for _, n := range benchPackageCounts {
		if n >= 10000 && testing.Short() {
			continue
		}

		xmlData, err := renderXML(WritePrimaryXML, benchRPMPackages(n))
		if err != nil {
			b.Fatal(err)
		}

		b.Run(fmt.Sprintf("primary/packages=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(xmlData)))

			for b.Loop() {
				if _, err := compressXML(xmlData); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPrimaryPipeline compares the worker's two ways of producing the
// compressed primary metadata: generate the whole XML then compress it, or
// stream the XML into the encoders. The 10000-package case is skipped under
// -short.
func BenchmarkPrimaryPipeline(b *testing.B) {
	for _, n := range benchPackageCounts {
		if n >= 10000 && testing.Short() {
			continue
		}

		pkgs := benchRPMPackages(n)

		b.Run(fmt.Sprintf("buffered/packages=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				xmlData, err := renderXML(WritePrimaryXML, pkgs)
				if err != nil {
					b.Fatal(err)
				}

				if _, err := compressXML(xmlData); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fmt.Sprintf("streamed/packages=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := CompressMetadataStream(func(w io.Writer) error { return WritePrimaryXML(w, pkgs) }, len(pkgs)*2048); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
