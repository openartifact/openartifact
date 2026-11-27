// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.9", "1.10", -1},
		{"1.0", "1.0.0", -1},
		{"1.0-1", "1.0.1", 0},
		{"0.9-3", "0.10-1", -1},
		{"2.0", "1.99.99", 1},
		{"01.2", "1.2", 0},
		{"99999999999999999999999.1", "99999999999999999999998.9", 1},
	}

	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}

		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}

	if !Less("1.0-1", "1.0.1") && !Less("1.0.1", "1.0-1") {
		t.Error("Less must break ties between equal spellings")
	}
}

func TestValidVersion(t *testing.T) {
	t.Parallel()

	for _, v := range []string{"1.0", "0.9-3", "2.1.0.9000", "1", "10-1"} {
		if !ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = false", v)
		}
	}

	for _, v := range []string{"", ".1", "1.", "1..2", "1.0a", "v1.0", "1.0_1", "1 0", strings.Repeat("1.", 40) + "1"} {
		if ValidVersion(v) {
			t.Errorf("ValidVersion(%q) = true", v)
		}
	}
}

func TestParsePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		kind Kind
		tree string
		file string
	}{
		{"src/contrib/PACKAGES", KindIndex, "src/contrib", ""},
		{"/src/contrib/PACKAGES.gz", KindIndex, "src/contrib", ""},
		{"src/contrib/PACKAGES.rds", KindIndex, "src/contrib", ""},
		{"src/contrib/glue_1.7.0.tar.gz", KindPackage, "src/contrib", "glue_1.7.0.tar.gz"},
		{"src/contrib/Archive", KindArchiveRoot, "src/contrib", ""},
		{"src/contrib/Archive/", KindArchiveRoot, "src/contrib", ""},
		{"src/contrib/Archive/glue", KindArchiveDir, "src/contrib", ""},
		{"src/contrib/Archive/glue/", KindArchiveDir, "src/contrib", ""},
		{"src/contrib/Archive/glue/glue_1.6.2.tar.gz", KindArchivePackage, "src/contrib", "glue_1.6.2.tar.gz"},
		{"bin/windows/contrib/4.4/PACKAGES.gz", KindIndex, "bin/windows/contrib/4.4", ""},
		{"bin/windows/contrib/4.4/glue_1.7.0.zip", KindPackage, "bin/windows/contrib/4.4", "glue_1.7.0.zip"},
		{"bin/macosx/contrib/4.3/glue_1.7.0.tgz", KindPackage, "bin/macosx/contrib/4.3", "glue_1.7.0.tgz"},
		{"bin/macosx/big-sur-arm64/contrib/4.4/PACKAGES", KindIndex, "bin/macosx/big-sur-arm64/contrib/4.4", ""},
	}

	for _, c := range cases {
		p, ok := ParsePath(c.in)
		if !ok || p.Kind != c.kind || p.Tree.Dir != c.tree || p.File != c.file {
			t.Errorf("ParsePath(%q) = %+v, %v; want kind %d tree %q file %q", c.in, p, ok, c.kind, c.tree, c.file)
		}
	}

	for _, in := range []string{
		"", "src", "src/contrib", "src/contrib/", "src/contrib/PACKAGES.xz", "src/contrib/glue.tar.gz",
		"src/contrib/glue_1.0.zip", "src/contrib/g_1.0.tar.gz", "src/contrib/../PACKAGES",
		"src/contrib/Archive/glue/other_1.0.tar.gz", "src/contrib/Archive/glue/x/glue_1.0.tar.gz",
		"bin/windows/contrib/4/PACKAGES", "bin/windows/contrib/4.4/glue_1.0.tgz", "bin/macosx/contrib/contrib/4.4/PACKAGES",
		"bin/macosx/A/contrib/4.4/PACKAGES", "bin/linux/contrib/4.4/PACKAGES", "Meta/archive.rds",
	} {
		if p, ok := ParsePath(in); ok {
			t.Errorf("ParsePath(%q) = %+v, want not ok", in, p)
		}
	}

	p, _ := ParsePath("src/contrib/Archive/glue/glue_1.6.2.tar.gz")
	if p.Canonical() != "src/contrib/glue_1.6.2.tar.gz" || p.Package != "glue" || p.Version != "1.6.2" {
		t.Errorf("archive path = %+v canonical %q", p, p.Canonical())
	}
}

const sampleDesc = `Package: demo
Version: 1.2-3
Title: A Demo
Depends: R (>= 3.5),
    utils
Imports: glue
License: MIT + file LICENSE
Description: Long text
    over lines.
NeedsCompilation: no
`

func TestReadRecords(t *testing.T) {
	t.Parallel()

	rec, err := ParseDescription(strings.NewReader(sampleDesc))
	if err != nil {
		t.Fatal(err)
	}

	if rec.Get("Package") != "demo" || rec.Get("Depends") != "R (>= 3.5),\nutils" || rec.Get("Description") != "Long text\nover lines." {
		t.Errorf("record = %+v", rec)
	}

	if got := StoredFields(rec, false)["Depends"]; got != "R (>= 3.5), utils" {
		t.Errorf("folded Depends = %q", got)
	}

	if _, has := StoredFields(rec, true)["NeedsCompilation"]; has {
		t.Error("binary trees carry no NeedsCompilation")
	}

	for name, in := range map[string]string{
		"duplicate":    "Package: a\nPackage: b\n",
		"no colon":     "Package a\n",
		"orphan cont":  " cont\n",
		"nul":          "Package: a\x00b\n",
		"two records":  "Package: a\n\nPackage: b\n",
		"empty":        "",
		"space in key": "Pack age: a\n",
	} {
		if _, err := ParseDescription(strings.NewReader(in)); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: err = %v, want ErrSyntax", name, err)
		}
	}

	if err := ReadRecords(
		strings.NewReader("A: "+strings.Repeat("x", 100)),
		50,
		func(Record) error { return nil },
	); !errors.Is(
		err,
		ErrTooLarge,
	) {
		t.Errorf("limit: err = %v", err)
	}

	if err := ReadRecords(
		strings.NewReader("A: "+strings.Repeat("x", maxLineBytes+1)),
		0,
		func(Record) error { return nil },
	); !errors.Is(
		err,
		ErrTooLarge,
	) {
		t.Errorf("long line: err = %v", err)
	}

	// Invalid UTF-8 (a latin1 DESCRIPTION) is repaired, not rejected.
	rec, err = ParseDescription(strings.NewReader("Package: a\nAuthor: Ren\xe9\n"))
	if err != nil || rec.Get("Author") != "Ren\uFFFD" {
		t.Errorf("latin1: %+v %v", rec, err)
	}
}

func TestRenderTextRoundTrip(t *testing.T) {
	t.Parallel()

	recs := []Record{
		IndexRecord(
			"b",
			"1.0",
			"d41d8cd98f00b204e9800998ecf8427e",
			map[string]string{"Depends": "R (>= 3.5), utils", "License": "GPL-2"},
			false,
		),
		IndexRecord("a", "0.1-2", "", nil, false),
	}

	text := RenderText(recs)

	want := "Package: b\nVersion: 1.0\nDepends: R (>= 3.5), utils\nLicense: GPL-2\nMD5sum: d41d8cd98f00b204e9800998ecf8427e\n\nPackage: a\nVersion: 0.1-2\n"
	if string(text) != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}

	back, err := ParsePackages(bytes.NewReader(text), 0)
	if err != nil || len(back) != 2 || back[0].Get("Depends") != "R (>= 3.5), utils" {
		t.Fatalf("round trip: %+v %v", back, err)
	}

	multi := RenderText([]Record{{{Name: "Package", Value: "x"}, {Name: "Depends", Value: "a,\nb"}}})
	if string(multi) != "Package: x\nDepends: a,\n    b\n" {
		t.Errorf("multi-line = %q", multi)
	}

	if _, err := ParsePackages(strings.NewReader("Package: a\n"), 0); !errors.Is(err, ErrSyntax) {
		t.Errorf("missing version: %v", err)
	}
}

func TestRenderRDSStructure(t *testing.T) {
	t.Parallel()

	recs := []Record{
		IndexRecord("a", "1.0", "abc", map[string]string{"Imports": "x"}, false),
		{{Name: "Package", Value: "caf\u00e9"}, {Name: "Version", Value: "2.0"}, {Name: "Extra", Value: "y"}},
	}

	rds, err := RenderRDS(recs, false)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := Gunzip(bytes.NewReader(rds), 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasPrefix(raw, []byte("X\n\x00\x00\x00\x02")) {
		t.Fatalf("header = %q", raw[:10])
	}

	// 16 columns (15 standard + Extra) by 2 rows, column-major; the second row of
	// the Package column is the UTF-8 flagged string.
	for _, needle := range []string{"dimnames", "dim", "Extra", "NeedsCompilation", "caf\u00e9"} {
		if !bytes.Contains(raw, []byte(needle)) {
			t.Errorf("RDS lacks %q", needle)
		}
	}

	if !bytes.Contains(raw, []byte{0, 0, 0, 9, 0xff, 0xff, 0xff, 0xff}) {
		t.Error("missing fields must be serialized as NA_character_")
	}
}

func tarGz(tb testing.TB, members map[string]string, order ...string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, name := range order {
		body := members[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			tb.Fatal(err)
		}

		if _, err := io.WriteString(tw, body); err != nil {
			tb.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}

	if err := gw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

func zipOf(tb testing.TB, members map[string]string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}

	sort.Strings(names)

	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			tb.Fatal(err)
		}

		if _, err := io.WriteString(w, members[n]); err != nil {
			tb.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

func TestReadDescription(t *testing.T) {
	t.Parallel()

	tgz := tarGz(t, map[string]string{"demo/R/f.R": "1", "demo/DESCRIPTION": sampleDesc}, "demo/R/f.R", "demo/DESCRIPTION")

	for ext, data := range map[string][]byte{
		".tar.gz": tgz,
		".tgz":    tgz,
		".zip":    zipOf(t, map[string]string{"demo/R/f.R": "1", "demo/DESCRIPTION": sampleDesc}),
	} {
		dir, rec, err := ReadDescription(bytes.NewReader(data), int64(len(data)), ext)
		if err != nil || dir != "demo" || rec.Get("Version") != "1.2-3" {
			t.Errorf("%s: dir %q rec %+v err %v", ext, dir, rec, err)
		}
	}

	nested := tarGz(
		t,
		map[string]string{"demo/inst/x/DESCRIPTION": sampleDesc, "DESCRIPTION": sampleDesc},
		"demo/inst/x/DESCRIPTION",
		"DESCRIPTION",
	)
	if _, _, err := ReadDescription(bytes.NewReader(nested), int64(len(nested)), ".tar.gz"); !errors.Is(err, ErrNoDescription) {
		t.Errorf("only nested DESCRIPTIONs: %v", err)
	}

	if _, _, err := ReadDescription(bytes.NewReader([]byte("not an archive")), 14, ".zip"); err == nil {
		t.Error("garbage zip accepted")
	}

	if _, _, err := ReadDescription(bytes.NewReader(nil), 0, ".rar"); err == nil {
		t.Error("unknown extension accepted")
	}
}

func TestLicenses(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"GPL-2 | GPL-3":                 {"GPL-2", "GPL-3"},
		"MIT + file LICENSE":            {"MIT"},
		"GPL (>= 2) | file LICENCE":     {"GPL (>= 2)"},
		"file LICENSE":                  nil,
		"":                              nil,
		"Apache License 2.0":            {"Apache License 2.0"},
		" BSD_3_clause + file LICENSE ": {"BSD_3_clause"},
	}

	for in, want := range cases {
		if got := Licenses(in); !slices.Equal(got, want) {
			t.Errorf("Licenses(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListing(t *testing.T) {
	t.Parallel()

	page := `<html><body><a href="?C=N;O=D">Name</a><a href="/">Parent</a><a href="../">up</a>` +
		`<ul><li><a href="glue/">glue/</a></li><li><a href="a&amp;b_1.0.tar.gz">x</a></li>` +
		`<a href="https://example.org/x">ext</a></ul></body></html>`

	got := Listing([]byte(page))
	want := []string{"glue/", "a&b_1.0.tar.gz"}

	if !slices.Equal(got, want) {
		t.Errorf("Listing = %q, want %q", got, want)
	}
}
