// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2_test

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/openartifact/openartifact/pkg/p2"
	"github.com/openartifact/openartifact/pkg/p2/p2test"
)

func run(t *testing.T, d p2.Doc, src []byte) ([]byte, error) {
	t.Helper()

	var out bytes.Buffer

	err := d.Rewrite(&out, bytes.NewReader(src), int64(len(src)), p2.Options{})

	return out.Bytes(), err
}

func TestRewriteEncodings(t *testing.T) {
	t.Parallel()

	jar, _ := p2.ParseDoc("artifacts.jar")
	xzd, _ := p2.ParseDoc("artifacts.xml.xz")
	plain, _ := p2.ParseDoc("artifacts.xml")

	plainOut, err := run(t, plain, []byte(p2test.Artifacts))
	if err != nil {
		t.Fatal(err)
	}

	jarOut, err := run(t, jar, p2test.Jar(t, "artifacts.xml", p2test.Artifacts))
	if err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(jarOut), int64(len(jarOut)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "artifacts.xml" {
		t.Fatalf("not a jar of artifacts.xml: %v", err)
	}

	rc, _ := zr.File[0].Open()
	inJar, _ := io.ReadAll(rc)
	_ = rc.Close()

	xzOut, err := run(t, xzd, p2test.XZ(t, p2test.Artifacts))
	if err != nil {
		t.Fatal(err)
	}

	xr, err := xz.NewReader(bytes.NewReader(xzOut))
	if err != nil {
		t.Fatal(err)
	}

	inXZ, _ := io.ReadAll(xr)

	for name, got := range map[string][]byte{"xml": plainOut, "jar": inJar, "xz": inXZ} {
		if !bytes.Equal(got, plainOut) {
			t.Errorf("%s: differs from the plain rewrite", name)
		}

		if bytes.Contains(got, []byte("mirrorsURL' value='https")) || bytes.Contains(got, []byte("statsURI")) {
			t.Errorf("%s: a repository property survived", name)
		}
	}
}

func TestRewriteArchiveRefusals(t *testing.T) {
	t.Parallel()

	jar, _ := p2.ParseDoc("artifacts.jar")
	xzd, _ := p2.ParseDoc("artifacts.xml.xz")

	if _, err := run(t, jar, p2test.Jar(t, "other.xml", p2test.Artifacts)); !errors.Is(err, p2.ErrNoEntry) {
		t.Errorf("a jar without the entry: got %v", err)
	}

	if _, err := run(t, jar, []byte("<html>not a zip</html>")); !errors.Is(err, p2.ErrBadArchive) {
		t.Errorf("not a zip: got %v", err)
	}

	if _, err := run(t, jar, nil); !errors.Is(err, p2.ErrBadArchive) {
		t.Errorf("empty jar: got %v", err)
	}

	if _, err := run(t, xzd, []byte("not xz at all")); err == nil {
		t.Error("not an xz stream was accepted")
	}

	if _, err := run(t, xzd, p2test.XZ(t, "<html/>")); !errors.Is(err, p2.ErrNotRepository) {
		t.Errorf("xz html: got %v", err)
	}

	// Two xz streams: the decoder would raise its dictionary for the second.
	two := append(p2test.XZ(t, p2test.Artifacts), p2test.XZ(t, p2test.Artifacts)...)
	if _, err := run(t, xzd, two); err == nil {
		t.Error("two concatenated xz streams were accepted")
	}
}

// TestJarEntryCountIsCheckedFirst builds an archive whose directory announces
// 60000 entries: it must be refused from the end record, not after the zip
// reader allocated a table for them.
func TestJarEntryCountIsCheckedFirst(t *testing.T) {
	t.Parallel()

	jar, _ := p2.ParseDoc("artifacts.jar")
	src := p2test.Jar(t, "artifacts.xml", p2test.Artifacts)

	eocd := bytes.LastIndex(src, []byte("PK\x05\x06"))
	if eocd < 0 {
		t.Fatal("no end record")
	}

	binary.LittleEndian.PutUint16(src[eocd+8:], 60000)
	binary.LittleEndian.PutUint16(src[eocd+10:], 60000)

	if _, err := run(t, jar, src); !errors.Is(err, p2.ErrBadArchive) || !strings.Contains(err.Error(), "60000") {
		t.Errorf("got %v, want the entry count refusal", err)
	}
}

func TestJarWithManyEntriesIsRefused(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for i := range 40 {
		w, _ := zw.Create(strings.Repeat("e", i+1))
		_, _ = w.Write([]byte("x"))
	}

	w, _ := zw.Create("artifacts.xml")
	_, _ = w.Write([]byte(p2test.Artifacts))
	_ = zw.Close()

	jar, _ := p2.ParseDoc("artifacts.jar")
	if _, err := run(t, jar, buf.Bytes()); !errors.Is(err, p2.ErrBadArchive) {
		t.Errorf("got %v, want a refusal", err)
	}
}

// TestDeclaredBombIsRefused patches the central directory of a jar to announce
// an entry of 1 GiB: it is refused from the header, before anything is inflated.
func TestDeclaredBombIsRefused(t *testing.T) {
	t.Parallel()

	jar, _ := p2.ParseDoc("artifacts.jar")
	src := p2test.Jar(t, "artifacts.xml", p2test.Artifacts)

	// The central directory header: "PK\x01\x02", uncompressed size at offset 24.
	cd := bytes.LastIndex(src, []byte("PK\x01\x02"))
	if cd < 0 {
		t.Fatal("no central directory")
	}

	binary.LittleEndian.PutUint32(src[cd+24:], 1<<30)

	if _, err := run(t, jar, src); err == nil {
		t.Fatal("an entry announcing 1 GiB was accepted")
	}
}
