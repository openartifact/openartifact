// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

const packagesFixture = "Package: foo\nVersion: 1.0-1\nArchitecture: amd64\nFilename: pool/main/f/foo/foo_1.0-1_amd64.deb\nSize: 10\nSHA256: aa\nDescription: first\n" +
	" continued line\n\n" +
	"package: Bar\r\nversion: 2:2.0\r\narchitecture: all\r\nfilename: pool/main/b/bar/bar_2.0_all.deb\r\nsha256: bb\r\n\r\n\r\n" +
	"Version: 9\nArchitecture: amd64\n\n" + // no Package field: skipped
	"Package: baz\nVersion: 3\nArchitecture: amd64\nFilename: pool/main/b/baz/baz_3_amd64.deb\nSHA256: cc"

func TestScanPackages(t *testing.T) {
	var got []Stanza

	err := ScanPackages(strings.NewReader(packagesFixture), func(s Stanza) error {
		got = append(got, s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("scanned %d stanzas, want 3: %+v", len(got), got)
	}

	foo := got[0]
	if foo.Package != "foo" || foo.Version != "1.0-1" || foo.Architecture != "amd64" || foo.Filename != "pool/main/f/foo/foo_1.0-1_amd64.deb" || foo.SHA256 != "aa" {
		t.Errorf("foo = %+v", foo)
	}

	if !strings.Contains(foo.Text, " continued line\n") || !strings.HasSuffix(foo.Text, "\n\n") {
		t.Errorf("foo text lost its continuation or blank-line terminator: %q", foo.Text)
	}

	bar := got[1]
	if bar.Package != "Bar" || bar.Version != "2:2.0" || bar.Architecture != "all" || bar.SHA256 != "bb" {
		t.Errorf("field names must match case-insensitively: %+v", bar)
	}

	if strings.Contains(bar.Text, "\r") {
		t.Errorf("CRLF not normalised: %q", bar.Text)
	}

	if got[2].Package != "baz" || !strings.HasSuffix(got[2].Text, "SHA256: cc\n\n") {
		t.Errorf("last stanza without trailing blank line: %+v", got[2])
	}
}

func TestScanPackagesStopsOnCallbackError(t *testing.T) {
	stop := io.ErrClosedPipe

	n := 0
	err := ScanPackages(strings.NewReader(packagesFixture), func(Stanza) error {
		n++
		return stop
	})

	if !errors.Is(err, stop) || n != 1 {
		t.Errorf("err = %v after %d stanzas, want the callback's error after 1", err, n)
	}
}

const releaseFixture = `Origin: Upstream
Suite: stable
Codename: bookworm
Architectures: amd64 arm64 all
Components: main contrib
MD5Sum:
 d41d8cd98f00b204e9800998ecf8427e 0 main/binary-amd64/Packages
SHA256:
 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 0 main/binary-amd64/Packages
 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 0 main/binary-amd64/Packages.xz
 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 0 contrib/binary-amd64/Packages.gz
`

func TestParseRelease(t *testing.T) {
	rel := ParseRelease([]byte(releaseFixture))

	if !slices.Equal(rel.Components, []string{"main", "contrib"}) || !slices.Equal(rel.Architectures, []string{"amd64", "arm64", "all"}) {
		t.Errorf("components %v architectures %v", rel.Components, rel.Architectures)
	}

	if !rel.HasIndex("main", "arm64") || rel.HasIndex("non-free", "amd64") || rel.HasIndex("main", "riscv64") {
		t.Error("HasIndex must reflect the declared components and architectures")
	}

	if got := rel.PackagesVariants("main", "amd64"); !slices.Equal(got, []string{"main/binary-amd64/Packages.xz", "main/binary-amd64/Packages"}) {
		t.Errorf("variants of main/amd64 = %v: want only the listed ones, best compression first", got)
	}

	if got := rel.PackagesVariants("main", "arm64"); len(got) != 3 || got[0] != "main/binary-arm64/Packages.xz" {
		t.Errorf("a Release listing none of the variants must fall back to all three, got %v", got)
	}
}

func TestParseReleaseClearsigned(t *testing.T) {
	signed := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\n" +
		strings.ReplaceAll(releaseFixture, "\nSHA256:", "\n- SHA256:") +
		"-----BEGIN PGP SIGNATURE-----\n\nabcdef\n-----END PGP SIGNATURE-----\n"

	rel := ParseRelease([]byte(signed))
	if !slices.Equal(rel.Components, []string{"main", "contrib"}) {
		t.Errorf("components of a clearsigned release = %v", rel.Components)
	}

	if _, ok := rel.Files["main/binary-amd64/Packages.xz"]; !ok {
		t.Errorf("dash-escaped hash section lost: %v", rel.Files)
	}
}

func TestDecompressIndex(t *testing.T) {
	var gzBuf, xzBuf bytes.Buffer

	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write([]byte(packagesFixture))
	_ = gw.Close()

	xw, _ := xz.NewWriter(&xzBuf)
	_, _ = xw.Write([]byte(packagesFixture))
	_ = xw.Close()

	for name, tc := range map[string]struct {
		path string
		data []byte
	}{
		"gz":    {"main/binary-amd64/Packages.gz", gzBuf.Bytes()},
		"xz":    {"main/binary-amd64/Packages.xz", xzBuf.Bytes()},
		"plain": {"main/binary-amd64/Packages", []byte(packagesFixture)},
	} {
		rc, err := DecompressIndex(io.NopCloser(bytes.NewReader(tc.data)), tc.path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		out, err := io.ReadAll(rc)
		_ = rc.Close()

		if err != nil || string(out) != packagesFixture {
			t.Errorf("%s: decompressed %d bytes, err %v", name, len(out), err)
		}
	}

	if _, err := DecompressIndex(io.NopCloser(strings.NewReader("not gzip")), "Packages.gz"); err == nil {
		t.Error("a corrupt gzip index must be an error")
	}
}
