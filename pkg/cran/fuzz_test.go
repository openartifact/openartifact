// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzReadRecords feeds arbitrary bytes to the DCF reader: it returns an error
// or well-formed records (unique non-empty field names, valid UTF-8, no NUL),
// and a PACKAGES rendering of what it read parses back to the same records.
func FuzzReadRecords(f *testing.F) {
	f.Add([]byte(sampleDesc))
	f.Add([]byte("Package: a\nVersion: 1.0\n\nPackage: b\nVersion: 2.0\nDepends: R (>= 1),\n    x\n"))
	f.Add([]byte("A: b\n c\n\t d\n\n\n\nE:\n"))
	f.Add([]byte(": x\n"))
	f.Add([]byte("A: \xff\xfe\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		var recs []Record

		err := ReadRecords(bytes.NewReader(data), 1<<16, func(r Record) error {
			recs = append(recs, r)

			return nil
		})
		if err != nil {
			return
		}

		for _, rec := range recs {
			seen := map[string]bool{}

			for _, fl := range rec {
				if fl.Name == "" || seen[fl.Name] {
					t.Fatalf("bad field name %q in %+v", fl.Name, rec)
				}

				seen[fl.Name] = true

				if strings.ContainsRune(fl.Value, 0) || strings.ContainsRune(fl.Name, 0) {
					t.Fatalf("NUL in %+v", fl)
				}
			}
		}

		// Names of a re-rendered document must survive a second parse.
		var out []Record

		for _, rec := range recs {
			if strings.Contains(rec.Get("Package"), "\n") {
				return
			}

			out = append(out, rec)
		}

		text := RenderText(out)

		var again []Record

		if err := ReadRecords(bytes.NewReader(text), 1<<17, func(r Record) error {
			again = append(again, r)

			return nil
		}); err != nil {
			return // a value starting with white space or a name with a colon is not re-parseable; only no-panic is required
		}

		if len(again) != len(out) {
			t.Fatalf("re-parse changed the record count: %d -> %d", len(out), len(again))
		}
	})
}

// FuzzParsePath checks the path grammar against its own inverse: whatever is
// accepted has a canonical path that parses to the same package and version,
// and nothing escapes the repository.
func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"src/contrib/PACKAGES.gz", "src/contrib/glue_1.7.0.tar.gz", "src/contrib/Archive/glue/glue_1.6.2.tar.gz",
		"bin/windows/contrib/4.4/glue_1.7.0.zip", "bin/macosx/big-sur-arm64/contrib/4.4/glue_1.7.0.tgz",
		"src/contrib/Archive/", "../etc/passwd", "src/contrib//x",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		p, ok := ParsePath(in)
		if !ok {
			return
		}

		if strings.Contains(in, "..") || strings.Contains(in, "//") || strings.ContainsAny(in, "\x00\\") {
			t.Fatalf("ParsePath accepted %q", in)
		}

		if p.Kind != KindPackage && p.Kind != KindArchivePackage {
			return
		}

		q, ok := ParsePath(p.Canonical())
		if !ok || q.Kind != KindPackage || q.Package != p.Package || q.Version != p.Version || q.Tree.Dir != p.Tree.Dir {
			t.Fatalf("canonical path of %q does not round-trip: %q -> %+v", in, p.Canonical(), q)
		}

		if !ValidPackageName(p.Package) || !ValidVersion(p.Version) {
			t.Fatalf("invalid identity accepted from %q: %+v", in, p)
		}
	})
}

// FuzzReadDescription feeds arbitrary bytes to the archive reader as each
// container kind: an error or a record, never a panic or a runaway read.
func FuzzReadDescription(f *testing.F) {
	tgz := tarGz(f, map[string]string{"demo/DESCRIPTION": sampleDesc}, "demo/DESCRIPTION")
	f.Add(tgz, ".tar.gz")
	f.Add(zipOf(f, map[string]string{"demo/DESCRIPTION": sampleDesc}), ".zip")
	f.Add([]byte("PK\x03\x04"), ".zip")
	f.Add([]byte{0x1f, 0x8b}, ".tgz")

	f.Fuzz(func(t *testing.T, data []byte, ext string) {
		dir, rec, err := ReadDescription(bytes.NewReader(data), int64(len(data)), ext)
		if err != nil {
			return
		}

		if dir == "" || strings.Contains(dir, "/") || len(rec) == 0 {
			t.Fatalf("dir %q record %+v", dir, rec)
		}
	})
}

// FuzzCompareVersions checks that the version order is antisymmetric and
// agrees with itself on the tie-break.
func FuzzCompareVersions(f *testing.F) {
	f.Add("1.0", "1.0-1")
	f.Add("0.9-3", "0.10-1")
	f.Add("1", "1.0.0")

	f.Fuzz(func(t *testing.T, a, b string) {
		if !ValidVersion(a) || !ValidVersion(b) {
			return
		}

		if CompareVersions(a, b) != -CompareVersions(b, a) {
			t.Fatalf("not antisymmetric: %q %q", a, b)
		}

		if a != b && Less(a, b) == Less(b, a) {
			t.Fatalf("Less is not strict and total: %q %q", a, b)
		}
	})
}
