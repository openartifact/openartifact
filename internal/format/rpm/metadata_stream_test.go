// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// nastyStrings exercise every branch of the XML escaping.
func nastyStrings() []string {
	return []string{
		"",
		"plain",
		`a<b>&"c'd`,
		"tab\tnl\ncr\rend",
		"ctrl\x00\x01\x08\x0b\x1f\x7f",
		"bad utf8 \xff\xfe trailing \xc3",
		"\xe2\x82",
		"replacement \uFFFD char",
		"non-char \uFFFE \uFFFF",
		"ünïcödé 日本語 😀",
		strings.Repeat("&", 100),
	}
}

func nastyPackage(rng *rand.Rand) pkgrpm.Package {
	nasty := nastyStrings()
	pick := func() string { return nasty[rng.IntN(len(nasty))] }

	deps := func(n int) []pkgrpm.Dependency {
		out := make([]pkgrpm.Dependency, n)
		flagSet := []uint32{
			0, pkgrpm.SENSE_EQUAL, pkgrpm.SENSE_LESS, pkgrpm.SENSE_GREATER,
			pkgrpm.SENSE_LESSEQUAL, pkgrpm.SENSE_GREATEREQUAL,
			pkgrpm.SENSE_SCRIPT_PRE, pkgrpm.SENSE_RPMLIB | pkgrpm.SENSE_LESSEQUAL,
			pkgrpm.SENSE_PREREQ | pkgrpm.SENSE_EQUAL,
		}

		for i := range out {
			ver := pick()
			if rng.IntN(2) == 0 {
				ver += "-" + pick()
			}

			out[i] = pkgrpm.Dependency{Name: pick() + "(GLIBC_2." + string(rune('0'+rng.IntN(9))) + ")", Flags: flagSet[rng.IntN(len(flagSet))], Version: ver, Epoch: uint32(rng.IntN(3))}
		}

		return out
	}

	p := pkgrpm.Package{
		Name: pick(), Version: pick(), Release: pick(), Arch: pick(), Epoch: uint32(rng.IntN(3)),
		Summary: pick(), Description: pick(), License: pick(), URL: pick(), Vendor: pick(),
		Group: pick(), Packager: pick(), SourceRPM: pick(), BuildHost: pick(), SHA256: pick(),
		BuildTime: rng.Int64N(1 << 40), PackageSize: rng.Int64N(1 << 30), InstalledSize: rng.Int64N(1 << 30),
		ArchiveSize: rng.Int64N(1 << 30), HeaderStart: rng.Int64N(1000), HeaderEnd: rng.Int64N(100000),
		Provides: deps(rng.IntN(4)), Requires: deps(rng.IntN(6)), Conflicts: deps(rng.IntN(2)),
		Obsoletes: deps(rng.IntN(2)), Suggests: deps(rng.IntN(2)), Enhances: deps(rng.IntN(2)),
		Recommends: deps(rng.IntN(2)), Supplements: deps(rng.IntN(2)),
	}

	for range rng.IntN(4) {
		p.Files = append(p.Files, pkgrpm.FileEntry{Path: pick() + "/" + pick()})
	}

	for range rng.IntN(4) {
		p.Changelogs = append(p.Changelogs, pkgrpm.ChangelogEntry{Time: rng.Int64N(1 << 31), Author: pick(), Description: pick()})
	}

	return p
}

func equivalenceFixtures() map[string][]pkgrpm.Package {
	rng := rand.New(rand.NewPCG(42, 43))

	nasty := make([]pkgrpm.Package, 300)
	for i := range nasty {
		nasty[i] = nastyPackage(rng)
	}

	onlyRPMLib := pkgrpm.Package{
		Name: "rpmlib-only", Version: "1", Release: "1", Arch: "noarch",
		Requires: []pkgrpm.Dependency{{Name: "rpmlib(PayloadIsZstd)", Flags: pkgrpm.SENSE_RPMLIB | pkgrpm.SENSE_LESSEQUAL, Version: "5.4-1"}},
	}

	return map[string][]pkgrpm.Package{
		"nil":            nil,
		"empty":          {},
		"minimal":        {{Name: "n", Version: "1", Release: "2"}},
		"only rpmlib":    {onlyRPMLib},
		"nasty":          nasty,
		"realistic":      benchRPMPackages(150),
		"realistic+edge": append(benchRPMPackages(5), onlyRPMLib),
	}
}

func TestStreamedXMLMatchesReflectionEncoder(t *testing.T) {
	generators := []struct {
		name string
		got  func([]pkgrpm.Package) ([]byte, error)
		want func([]pkgrpm.Package) ([]byte, error)
	}{
		{
			"primary",
			func(p []pkgrpm.Package) ([]byte, error) { return renderXML(WritePrimaryXML, p) },
			func(p []pkgrpm.Package) ([]byte, error) { return refGeneratePrimaryXML(p, "") },
		},
		{
			"filelists",
			func(p []pkgrpm.Package) ([]byte, error) { return renderXML(WriteFilelistsXML, p) },
			refGenerateFilelistsXML,
		},
		{
			"other",
			func(p []pkgrpm.Package) ([]byte, error) { return renderXML(WriteOtherXML, p) },
			refGenerateOtherXML,
		},
	}

	for fixture, pkgs := range equivalenceFixtures() {
		for _, g := range generators {
			t.Run(g.name+"/"+fixture, func(t *testing.T) {
				want, err := g.want(pkgs)
				if err != nil {
					t.Fatal(err)
				}

				got, err := g.got(pkgs)
				if err != nil {
					t.Fatal(err)
				}

				if !bytes.Equal(want, got) {
					t.Fatalf("output differs at byte %d\nwant: %q\ngot:  %q", firstDiff(want, got), excerpt(want, got), excerpt(got, want))
				}
			})
		}
	}
}

func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}

	return n
}

func excerpt(a, b []byte) string {
	i := firstDiff(a, b)
	lo := max(0, i-60)
	hi := min(len(a), i+60)

	return string(a[lo:hi])
}

// failAfter fails every Write once n bytes have been accepted.
type failAfter struct {
	n   int
	err error
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n < len(p) {
		return 0, f.err
	}

	f.n -= len(p)

	return len(p), nil
}

func TestStreamedXMLPropagatesWriteErrors(t *testing.T) {
	boom := errors.New("disk full")
	pkgs := benchRPMPackages(50)

	writers := map[string]func(io.Writer, []pkgrpm.Package) error{
		"primary":   WritePrimaryXML,
		"filelists": WriteFilelistsXML,
		"other":     WriteOtherXML,
	}

	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			if err := write(&failAfter{n: 1000, err: boom}, pkgs); !errors.Is(err, boom) {
				t.Fatalf("want write error, got %v", err)
			}
		})
	}
}

func TestStreamedXMLFlushesInChunks(t *testing.T) {
	var sizes []int

	w := writerFunc(func(p []byte) (int, error) {
		sizes = append(sizes, len(p))
		return len(p), nil
	})

	if err := WritePrimaryXML(w, benchRPMPackages(200)); err != nil {
		t.Fatal(err)
	}

	if len(sizes) < 2 {
		t.Fatalf("expected multiple chunked writes, got %v", sizes)
	}

	for _, n := range sizes[:len(sizes)-1] {
		if n < flushThreshold {
			t.Fatalf("intermediate chunk of %d bytes is below flush threshold", n)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
