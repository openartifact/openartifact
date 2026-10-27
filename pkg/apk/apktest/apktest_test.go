// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apktest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/apk"
)

// member is one gzip member of a package, taken apart without apk's parser.
type member struct {
	compressed []byte
	files      []file
	end        bool // the tar has end-of-archive blocks
}

type file struct {
	hdr  tar.Header
	body string
}

func members(t *testing.T, data []byte) []member {
	t.Helper()

	rd := bytes.NewReader(data) // a ByteReader: the gzip reader stops at the end of the member

	zr, err := gzip.NewReader(rd)
	if err != nil {
		t.Fatal(err)
	}

	var out []member

	for start := 0; ; {
		zr.Multistream(false)

		raw, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}

		end := len(data) - rd.Len()
		out = append(out, member{compressed: data[start:end], files: readTar(t, raw), end: bytes.HasSuffix(raw, make([]byte, 1024))})

		if rd.Len() == 0 {
			return out
		}

		start = end

		if err := zr.Reset(rd); err != nil {
			t.Fatal(err)
		}
	}
}

func readTar(t *testing.T, raw []byte) []file {
	t.Helper()

	var files []file

	tr := tar.NewReader(bytes.NewReader(raw))

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}

		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}

		files = append(files, file{hdr: *hdr, body: string(body)})
	}
}

func parse(t *testing.T, b []byte) *apk.Package {
	t.Helper()

	p, err := apk.ParsePackage(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestBuild_defaults(t *testing.T) {
	b := Build(t, Options{Name: "hello"})

	got := parse(t, b)

	want := &apk.Package{
		Name: "hello", Version: "1.0-r0", Arch: "x86_64", Description: "test package", License: "MIT", Origin: "hello",
		BuildTime: 1700000000, InstalledSize: int64(len("hello 1.0-r0\n")), Size: int64(len(b)),
		DataHash: got.DataHash, Checksum: got.Checksum,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}

	ms := members(t, b)
	if len(ms) != 2 {
		t.Fatalf("%d gzip members, want control and data", len(ms))
	}

	files := ms[1].files
	if len(files) != 4 || files[0].hdr.Name != "usr/" || files[1].hdr.Name != "usr/share/" ||
		files[2].hdr.Name != "usr/share/hello/" || files[3].hdr.Name != "usr/share/hello/README" || files[3].body != "hello 1.0-r0\n" {
		t.Fatalf("data entries %+v", files)
	}
}

func TestBuild_options(t *testing.T) {
	key, err := apk.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := apk.NewSigner("apktest.rsa.pub", key)
	if err != nil {
		t.Fatal(err)
	}

	o := Options{
		Name: "libfoo-dev", Version: "2.3.4_rc1-r7", Arch: "aarch64", Description: "Foo headers", License: "GPL-2.0-only",
		Origin: "libfoo", URL: "https://example.org/foo", Depends: []string{"libfoo=2.3.4_rc1-r7", "so:libc.musl-aarch64.so.1"},
		Provides: []string{"pc:foo=2.3.4"},
		Files:    map[string]string{"usr/include/foo.h": "#define FOO 1\n", "usr/lib/pkgconfig/foo.pc": "Name: foo\n", "etc/foo.conf": ""},
		Sign:     signer,
	}

	b := Build(t, o)

	got := parse(t, b)
	if got.Name != o.Name || got.Version != o.Version || got.Arch != o.Arch || got.Description != o.Description ||
		got.License != o.License || got.Origin != o.Origin || got.URL != o.URL || !got.Signed ||
		!reflect.DeepEqual(got.Depends, o.Depends) || !reflect.DeepEqual(got.Provides, o.Provides) ||
		got.InstalledSize != int64(len("#define FOO 1\nName: foo\n")) {
		t.Fatalf("got %+v", got)
	}

	ms := members(t, b)
	if len(ms) != 3 {
		t.Fatalf("%d gzip members, want signature, control and data", len(ms))
	}

	checkSignature(t, ms[0], ms[1], &key.PublicKey)
	checkControl(t, ms[1])
	checkData(t, ms[2])
}

func checkSignature(t *testing.T, sig, control member, pub *rsa.PublicKey) {
	t.Helper()

	if len(sig.files) != 1 || sig.files[0].hdr.Name != ".SIGN.RSA256.apktest.rsa.pub" || sig.end {
		t.Fatalf("signature member %+v (end blocks %v), want one .SIGN.RSA256. entry and no end blocks", sig.files, sig.end)
	}

	sum := sha256.Sum256(control.compressed)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], []byte(sig.files[0].body)); err != nil {
		t.Fatalf("signature does not cover the control segment: %v", err)
	}
}

func checkControl(t *testing.T, control member) {
	t.Helper()

	if control.end || len(control.files) != 1 || control.files[0].hdr.Name != ".PKGINFO" {
		t.Fatalf("control member %+v", control)
	}

	info := control.files[0].body
	for _, line := range []string{"depend = libfoo=2.3.4_rc1-r7\n", "provides = pc:foo=2.3.4\n", "url = https://example.org/foo\n"} {
		if !strings.Contains(info, line) {
			t.Fatalf("PKGINFO has no %q:\n%s", line, info)
		}
	}
}

func checkData(t *testing.T, data member) {
	t.Helper()

	var names []string

	for _, f := range data.files {
		names = append(names, f.hdr.Name)

		if f.hdr.Typeflag != tar.TypeReg {
			continue
		}

		sum := sha1.Sum([]byte(f.body))
		if got := f.hdr.PAXRecords["APK-TOOLS.checksum.SHA1"]; got != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: SHA-1 record %q", f.hdr.Name, got)
		}
	}

	want := []string{
		"etc/", "usr/", "usr/include/", "usr/lib/", "usr/lib/pkgconfig/",
		"etc/foo.conf", "usr/include/foo.h", "usr/lib/pkgconfig/foo.pc",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("data entries %v, want %v", names, want)
	}

	if !data.end {
		t.Fatal("data tar has no end-of-archive blocks")
	}
}

func TestBuild_deterministic(t *testing.T) {
	key, err := apk.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := apk.NewSigner("apktest.rsa.pub", key)
	if err != nil {
		t.Fatal(err)
	}

	o := Options{Name: "a", Files: map[string]string{"x/1": "1", "x/2": "2", "y/z/3": "3", "w": "4"}, Sign: signer}

	first := Build(t, o)
	for range 5 {
		if !bytes.Equal(first, Build(t, o)) {
			t.Fatal("two builds of the same options differ")
		}
	}

	if bytes.Equal(first, Build(t, Options{Name: "b"})) {
		t.Fatal("different options gave the same package")
	}
}

// recorder is a testing.TB that records the first failure and stops the
// goroutine, as t.Fatal does.
type recorder struct {
	testing.TB

	msg string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatal(args ...any) {
	r.msg = fmt.Sprint(args...)

	runtime.Goexit()
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)

	runtime.Goexit()
}

func TestBuild_invalid(t *testing.T) {
	tests := []struct {
		name string
		o    Options
		want string
	}{
		{"no name", Options{}, "Options.Name is required"},
		{"bad name", Options{Name: "../x"}, "pkgname"},
		{"bad version", Options{Name: "a", Version: "v1"}, "pkgver"},
		{"bad arch", Options{Name: "a", Arch: "x86-64"}, "arch"},
		{"description with a newline", Options{Name: "a", Description: "a\nb"}, "malformed .PKGINFO"},
		{"empty dependency", Options{Name: "a", Depends: []string{""}}, "depend"},
		{"absolute path", Options{Name: "a", Files: map[string]string{"/etc/x": "x"}}, "escapes the install root"},
		{"path outside the root", Options{Name: "a", Files: map[string]string{"../x": "x"}}, "escapes the install root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recorder{TB: t}
			done := make(chan struct{})

			go func() {
				defer close(done)

				Build(r, tt.o)
			}()

			<-done

			if !strings.Contains(r.msg, tt.want) {
				t.Fatalf("failure %q does not mention %q", r.msg, tt.want)
			}
		})
	}
}
