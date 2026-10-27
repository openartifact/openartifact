// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// gzMember is one gzip member of an archive, taken apart without using any
// code of this package.
type gzMember struct {
	compressed []byte
	raw        []byte
	header     gzip.Header
}

func splitMembers(t *testing.T, data []byte) []gzMember {
	t.Helper()

	rd := bytes.NewReader(data) // a ByteReader: the gzip reader stops at the member's end

	zr, err := gzip.NewReader(rd)
	if err != nil {
		t.Fatal(err)
	}

	var members []gzMember

	for start := 0; ; {
		zr.Multistream(false)

		raw, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}

		end := len(data) - rd.Len()
		members = append(members, gzMember{compressed: data[start:end], raw: raw, header: zr.Header})

		if rd.Len() == 0 {
			return members
		}

		start = end

		if err := zr.Reset(rd); err != nil {
			t.Fatal(err)
		}
	}
}

type tarFile struct {
	hdr  tar.Header
	body []byte
}

func readTar(t *testing.T, raw []byte) []tarFile {
	t.Helper()

	var files []tarFile

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

		files = append(files, tarFile{hdr: *hdr, body: body})
	}
}

func TestBuildIndex_layout(t *testing.T) {
	pkgs := samplePackages()

	data, err := BuildIndex(pkgs, "v3.20.0-test", testSigner(t))
	if err != nil {
		t.Fatal(err)
	}

	members := splitMembers(t, data)
	if len(members) != 2 {
		t.Fatalf("%d gzip members, want a signature and an index", len(members))
	}

	for i, m := range members {
		checkGzipHeader(t, i, m)
	}

	checkSignatureMember(t, members[0], members[1])
	checkIndexMember(t, members[1], pkgs)
}

func checkGzipHeader(t *testing.T, i int, m gzMember) {
	t.Helper()

	if !m.header.ModTime.IsZero() || m.header.Name != "" || m.header.Comment != "" || m.header.OS != 3 {
		t.Errorf("member %d: gzip header %+v, want mtime 0, no name, OS 3", i, m.header)
	}

	if !bytes.Equal(m.compressed[:4], []byte{0x1f, 0x8b, 8, 0}) || !bytes.Equal(m.compressed[4:8], make([]byte, 4)) {
		t.Errorf("member %d: gzip header bytes % x", i, m.compressed[:10])
	}
}

func checkSignatureMember(t *testing.T, sigMember, indexMember gzMember) {
	t.Helper()

	files := readTar(t, sigMember.raw)
	if len(files) != 1 || files[0].hdr.Name != ".SIGN.RSA256.openartifact-test.rsa.pub" {
		t.Fatalf("signature entries %+v", files)
	}

	// As in the index apk itself publishes, the signature's tar has no
	// end-of-archive blocks: header and the padded signature, nothing else.
	if want := 512 + (len(files[0].body)+511)/512*512; len(sigMember.raw) != want {
		t.Fatalf("signature tar is %d bytes, want %d with no end blocks", len(sigMember.raw), want)
	}

	sum := sha256.Sum256(indexMember.compressed)
	if err := rsa.VerifyPKCS1v15(&testKey(t).PublicKey, crypto.SHA256, sum[:], files[0].body); err != nil {
		t.Fatalf("signature does not cover the bytes of the index member: %v", err)
	}

	checkHeader(t, &files[0].hdr)
}

func checkIndexMember(t *testing.T, indexMember gzMember, pkgs []Package) {
	t.Helper()

	files := readTar(t, indexMember.raw)
	if len(files) != 2 || files[0].hdr.Name != "DESCRIPTION" || files[1].hdr.Name != "APKINDEX" {
		t.Fatalf("index entries %+v", files)
	}

	if string(files[0].body) != "v3.20.0-test" {
		t.Fatalf("DESCRIPTION %q", files[0].body)
	}

	if !bytes.Equal(files[1].body, RenderIndex(pkgs)) {
		t.Fatalf("APKINDEX differs from RenderIndex")
	}

	// The index tar is terminated, again as in the official index.
	if !bytes.HasSuffix(indexMember.raw, make([]byte, 1024)) {
		t.Fatal("index tar has no end-of-archive blocks")
	}

	for i := range files {
		checkHeader(t, &files[i].hdr)
	}
}

func checkHeader(t *testing.T, h *tar.Header) {
	t.Helper()

	if h.Typeflag != tar.TypeReg || h.Mode != 0o644 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" ||
		h.ModTime.Unix() != 0 || h.Format != tar.FormatUSTAR || len(h.PAXRecords) != 0 {
		t.Errorf("%s: header %+v, want a plain ustar regular file, mode 0644, owner 0, mtime 0", h.Name, *h)
	}
}

func TestBuildIndex_deterministic(t *testing.T) {
	a, err := BuildIndex(samplePackages(), "d", testSigner(t))
	if err != nil {
		t.Fatal(err)
	}

	b, err := BuildIndex(samplePackages(), "d", testSigner(t))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(a, b) {
		t.Fatal("two builds of the same index differ")
	}

	c, err := BuildIndex(samplePackages()[:1], "d", testSigner(t))
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(a, c) {
		t.Fatal("different packages gave the same index")
	}
}

func TestBuildIndex_unsigned(t *testing.T) {
	signed, err := BuildIndex(samplePackages(), "d", testSigner(t))
	if err != nil {
		t.Fatal(err)
	}

	unsigned, err := BuildIndex(samplePackages(), "d", nil)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasSuffix(signed, unsigned) || len(signed) == len(unsigned) {
		t.Fatal("the signed index is not the signature member followed by the unsigned index")
	}

	if n := len(splitMembers(t, unsigned)); n != 1 {
		t.Fatalf("%d members", n)
	}
}

func TestBuildIndex_parses(t *testing.T) {
	want := samplePackages()

	for name, signer := range map[string]*Signer{"signed": testSigner(t), "unsigned": nil} {
		t.Run(name, func(t *testing.T) {
			data, err := BuildIndex(want, "d", signer)
			if err != nil {
				t.Fatal(err)
			}

			got, err := ParseIndex(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got  %+v\nwant %+v", got, want)
			}
		})
	}

	t.Run("no packages", func(t *testing.T) {
		data, err := BuildIndex(nil, "", testSigner(t))
		if err != nil {
			t.Fatal(err)
		}

		got, err := ParseIndex(bytes.NewReader(data))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}

		if _, err := VerifyIndex(data, trusted(t)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBuildIndex_invalid(t *testing.T) {
	edit := func(f func(*Package)) []Package {
		pkgs := samplePackages()
		f(&pkgs[0])

		return pkgs
	}

	tests := []struct {
		name string
		pkgs []Package
		desc string
	}{
		{"description with a newline", samplePackages(), "a\nb"},
		{"long description", samplePackages(), strings.Repeat("d", 1025)},
		{"description with invalid UTF-8", samplePackages(), "a\xff"},
		{"bad name", edit(func(p *Package) { p.Name = "../x" }), "d"},
		{"bad version", edit(func(p *Package) { p.Version = "x" }), "d"},
		{"bad arch", edit(func(p *Package) { p.Arch = "x/y" }), "d"},
		{"no checksum", edit(func(p *Package) { p.Checksum = "" }), "d"},
		{"bad checksum", edit(func(p *Package) { p.Checksum = "Q1x" }), "d"},
		{"control character in a field", edit(func(p *Package) { p.Description = "a\nC:x" }), "d"},
		{"control character in a dependency", edit(func(p *Package) { p.Depends = []string{"a\x01"} }), "d"},
		{"dependency with a space", edit(func(p *Package) { p.Depends = []string{"a b"} }), "d"},
		{"empty dependency", edit(func(p *Package) { p.Provides = []string{""} }), "d"},
		{"negative size", edit(func(p *Package) { p.Size = -1 }), "d"},
		{"negative priority", edit(func(p *Package) { p.ProviderPriority = -1 }), "d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildIndex(tt.pkgs, tt.desc, testSigner(t))
			if !errors.Is(err, ErrInvalidIndex) || got != nil {
				t.Fatalf("got %d bytes, %v, want ErrInvalidIndex", len(got), err)
			}
		})
	}
}

func trusted(t *testing.T) map[string]*rsa.PublicKey {
	t.Helper()

	return map[string]*rsa.PublicKey{
		"openartifact-test.rsa.pub": &testKey(t).PublicKey,
		"other.rsa.pub":             &otherKey(t).PublicKey,
	}
}

func TestVerifyIndex_valid(t *testing.T) {
	built, err := BuildIndex(samplePackages(), "d", testSigner(t))
	if err != nil {
		t.Fatal(err)
	}

	index := splitMembers(t, built)[1].compressed

	tests := []struct {
		name string
		data []byte
	}{
		{"built by BuildIndex", built},
		{"SHA-256, index tar without end blocks", concat(sigMember(t, "sha256", index), index)},
		{"SHA-1 as Alpine's own indexes", concat(sigMember(t, "sha1", index), index)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VerifyIndex(tt.data, trusted(t))
			if err != nil || got != "openartifact-test.rsa.pub" {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}

	t.Run("signature member with end blocks", func(t *testing.T) {
		sig := gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.other.rsa.pub", body: signRaw(t, otherKey(t), index)}}, true))

		got, err := VerifyIndex(concat(sig, index), trusted(t))
		if err != nil || got != "other.rsa.pub" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

func signRaw(t *testing.T, key *rsa.PrivateKey, signed []byte) string {
	t.Helper()

	sum := sha256.Sum256(signed)

	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}

	return string(sig)
}

func TestVerifyIndex_invalid(t *testing.T) {
	built, err := BuildIndex(samplePackages(), "d", testSigner(t))
	if err != nil {
		t.Fatal(err)
	}

	members := splitMembers(t, built)
	sig, index := members[0].compressed, members[1].compressed

	flip := func(b []byte, i int) []byte {
		out := append([]byte(nil), b...)
		out[i] ^= 0x01

		return out
	}

	unsigned, err := BuildIndex(samplePackages(), "d", nil)
	if err != nil {
		t.Fatal(err)
	}

	otherSigned, err := NewSigner("openartifact-test.rsa.pub", otherKey(t))
	if err != nil {
		t.Fatal(err)
	}

	byOther, err := BuildIndex(samplePackages(), "d", otherSigned)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data []byte
		keys map[string]*rsa.PublicKey
	}{
		{"index byte changed", flip(built, len(sig)+len(index)/2), trusted(t)},
		{"first byte of the index member changed", flip(built, len(sig)+2), trusted(t)},
		{"last byte of the index member changed", flip(built, len(built)-1), trusted(t)},
		{"bytes appended", concat(built, []byte{0}), trusted(t)},
		{"a member appended", concat(built, index), trusted(t)},
		{"index truncated", built[:len(built)-1], trusted(t)},
		{"different index under the same signature", concat(sig, mustIndex(t, samplePackages()[:1])), trusted(t)},
		{"signed by another key under the same name", byOther, trusted(t)},
		{"key not trusted", built, map[string]*rsa.PublicKey{"other.rsa.pub": &otherKey(t).PublicKey}},
		{"no keys", built, map[string]*rsa.PublicKey{}},
		{"nil keys", built, nil},
		{"nil public key", built, map[string]*rsa.PublicKey{"openartifact-test.rsa.pub": nil}},
		{"unsigned index", unsigned, trusted(t)},
		{"signature only", sig, trusted(t)},
		{"empty", nil, trusted(t)},
		{"not gzip", []byte("APKINDEX"), trusted(t)},
		{"plain text index", RenderIndex(samplePackages()), trusted(t)},
		{"signature member with two entries", concat(gz(t, tarBytes(t, []tarEntry{
			{name: ".SIGN.RSA256.openartifact-test.rsa.pub", body: "x"}, {name: ".SIGN.RSA256.other.rsa.pub", body: "x"},
		}, false)), index), trusted(t)},
		{
			"signature entry that is not a signature",
			concat(gz(t, tarBytes(t, []tarEntry{{name: "APKINDEX", body: "x"}}, false)), index),
			trusted(t),
		},
		{
			"unsupported algorithm",
			concat(gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.DSA.openartifact-test.rsa.pub", body: "x"}}, false)), index),
			trusted(t),
		},
		{"no key name", concat(gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.", body: "x"}}, false)), index), trusted(t)},
		{
			"key name with a path",
			concat(gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.../x.rsa.pub", body: "x"}}, false)), index),
			trusted(t),
		},
		{
			"signature that is a symlink",
			concat(
				gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.openartifact-test.rsa.pub", typ: tar.TypeSymlink, link: "x"}}, false)),
				index,
			),
			trusted(t),
		},
		{
			"empty signature",
			concat(gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.openartifact-test.rsa.pub"}}, false)), index),
			trusted(t),
		},
		{
			"signature of 5000 bytes",
			concat(
				gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.openartifact-test.rsa.pub", body: strings.Repeat("s", 5000)}}, false)),
				index,
			),
			trusted(t),
		},
		{
			"signature member of 100 KiB",
			concat(
				gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.openartifact-test.rsa.pub", body: strings.Repeat("s", 100<<10)}}, false)),
				index,
			),
			trusted(t),
		},
	}

	reasons := map[string]string{
		"index byte changed":                        "verification error",
		"first byte of the index member changed":    "verification error",
		"last byte of the index member changed":     "verification error",
		"bytes appended":                            "verification error",
		"a member appended":                         "verification error",
		"index truncated":                           "verification error",
		"different index under the same signature":  "verification error",
		"signed by another key under the same name": "verification error",
		"key not trusted":                           "no trusted key",
		"no keys":                                   "no trusted key",
		"nil keys":                                  "no trusted key",
		"nil public key":                            "no trusted key",
		"unsigned index":                            "not signed",
		"signature only":                            "nothing follows",
		"empty":                                     "does not start with a gzip member",
		"not gzip":                                  "does not start with a gzip member",
		"plain text index":                          "does not start with a gzip member",
		"signature member with two entries":         "more than one entry",
		"signature entry that is not a signature":   "not signed",
		"unsupported algorithm":                     "unsupported signature",
		"no key name":                               "no trusted key",
		"key name with a path":                      "no trusted key",
		"signature that is a symlink":               "malformed signature entry",
		"empty signature":                           "verification error",
		"signature of 5000 bytes":                   "malformed signature entry",
		"signature member of 100 KiB":               "unreadable or too large",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, ok := reasons[tt.name]
			if !ok {
				t.Fatal("no expected reason")
			}

			got, err := VerifyIndex(tt.data, tt.keys)
			if !errors.Is(err, ErrSignature) || got != "" {
				t.Fatalf("got %q, %v, want ErrSignature", got, err)
			}

			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not mention %q", err, want)
			}
		})
	}
}

func mustIndex(t *testing.T, pkgs []Package) []byte {
	t.Helper()

	data, err := BuildIndex(pkgs, "d", nil)
	if err != nil {
		t.Fatal(err)
	}

	return data
}
