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
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/apk"
)

const keyName = "openartifact-test.rsa.pub"

// One RSA key generation per test binary: it is the slow part.
//
//nolint:gochecknoglobals // see above
var (
	newKey      = sync.OnceValues(func() (*rsa.PrivateKey, error) { return apk.GenerateKey(2048) })
	newOtherKey = sync.OnceValues(func() (*rsa.PrivateKey, error) { return apk.GenerateKey(2048) })
)

func testKey(tb testing.TB) *rsa.PrivateKey {
	tb.Helper()

	key, err := newKey()
	if err != nil {
		tb.Fatal(err)
	}

	return key
}

func otherKey(tb testing.TB) *rsa.PrivateKey {
	tb.Helper()

	key, err := newOtherKey()
	if err != nil {
		tb.Fatal(err)
	}

	return key
}

func signer(tb testing.TB, key *rsa.PrivateKey) *apk.Signer {
	tb.Helper()

	s, err := apk.NewSigner(keyName, key)
	if err != nil {
		tb.Fatal(err)
	}

	return s
}

func trusted(tb testing.TB) map[string]*rsa.PublicKey {
	tb.Helper()

	return map[string]*rsa.PublicKey{keyName: &testKey(tb).PublicKey, "other.rsa.pub": &otherKey(tb).PublicKey}
}

func samplePackages() []apk.Package {
	return []apk.Package{
		{Name: "hello", Version: "1.2.3-r0", Arch: "x86_64", Checksum: "Q1qvTGHdzF6KLavt4PO0gs2a6pQ00=", Size: 1234, InstalledSize: 21},
		{Name: "min", Version: "1", Arch: "noarch", Checksum: "Q1tsk11PPHsiD6A4YTofnBtWslWoY="},
	}
}

func buildIndex(tb testing.TB, pkgs []apk.Package, s *apk.Signer) []byte {
	tb.Helper()

	data, err := apk.BuildIndex(pkgs, "d", s)
	if err != nil {
		tb.Fatal(err)
	}

	return data
}

// sigEntry is one tar entry of a hand-made signature member.
type sigEntry struct {
	name, body, link string
	typ              byte
}

// sigMember returns a gzip member whose tar, without end blocks, holds entries.
func sigMember(tb testing.TB, entries ...sigEntry) []byte {
	tb.Helper()

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		hdr := &tar.Header{
			Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg, ModTime: time.Unix(1700000000, 0), Linkname: e.link,
		}
		if e.typ != 0 {
			hdr.Typeflag, hdr.Size = e.typ, 0
		}

		if err := tw.WriteHeader(hdr); err != nil {
			tb.Fatal(err)
		}

		if _, err := tw.Write([]byte(e.body)); err != nil {
			tb.Fatal(err)
		}
	}

	if err := tw.Flush(); err != nil {
		tb.Fatal(err)
	}

	if err := gw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// signedBy returns the signature member for index; sha1Sum selects the older
// .SIGN.RSA. entry Alpine's own indexes carry.
func signedBy(tb testing.TB, key *rsa.PrivateKey, name string, sha1Sum bool, index []byte) []byte {
	tb.Helper()

	var (
		entryName string
		digest    []byte
		alg       crypto.Hash
	)

	if sha1Sum {
		sum := sha1.Sum(index)
		entryName, digest, alg = ".SIGN.RSA."+name, sum[:], crypto.SHA1
	} else {
		sum := sha256.Sum256(index)
		entryName, digest, alg = ".SIGN.RSA256."+name, sum[:], crypto.SHA256
	}

	sig, err := rsa.SignPKCS1v15(nil, key, alg, digest)
	if err != nil {
		tb.Fatal(err)
	}

	return sigMember(tb, sigEntry{name: entryName, body: string(sig)})
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}

	return out
}

func TestVerifyIndex_valid(t *testing.T) {
	built := buildIndex(t, samplePackages(), signer(t, testKey(t)))
	index := members(t, built)[1].compressed

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"built by BuildIndex", built, keyName},
		{"SHA-256", concat(signedBy(t, testKey(t), keyName, false, index), index), keyName},
		{"SHA-1 as Alpine's own indexes", concat(signedBy(t, testKey(t), keyName, true, index), index), keyName},
		{"another trusted key", concat(signedBy(t, otherKey(t), "other.rsa.pub", false, index), index), "other.rsa.pub"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VerifyIndex(tt.data, trusted(t))
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}

	t.Run("no packages", func(t *testing.T) {
		if _, err := VerifyIndex(buildIndex(t, nil, signer(t, testKey(t))), trusted(t)); err != nil {
			t.Fatal(err)
		}
	})
}

func flip(b []byte, i int) []byte {
	out := append([]byte(nil), b...)
	out[i] ^= 0x01

	return out
}

func TestVerifyIndex_invalid(t *testing.T) {
	built := buildIndex(t, samplePackages(), signer(t, testKey(t)))
	ms := members(t, built)
	sig, index := ms[0].compressed, ms[1].compressed
	unsigned := buildIndex(t, samplePackages(), nil)
	byOther := buildIndex(t, samplePackages(), signer(t, otherKey(t)))
	other := map[string]*rsa.PublicKey{"other.rsa.pub": &otherKey(t).PublicKey}
	bogus := func(e ...sigEntry) []byte { return concat(sigMember(t, e...), index) }
	name := ".SIGN.RSA256." + keyName

	tests := []struct {
		name string
		data []byte
		keys map[string]*rsa.PublicKey
		want string
	}{
		{"index byte changed", flip(built, len(sig)+len(index)/2), trusted(t), "verification error"},
		{"first byte of the index member changed", flip(built, len(sig)+2), trusted(t), "verification error"},
		{"last byte of the index member changed", flip(built, len(built)-1), trusted(t), "verification error"},
		{"bytes appended", concat(built, []byte{0}), trusted(t), "verification error"},
		{"a member appended", concat(built, index), trusted(t), "verification error"},
		{"index truncated", built[:len(built)-1], trusted(t), "verification error"},
		{
			"different index under the same signature",
			concat(sig, buildIndex(t, samplePackages()[:1], nil)), trusted(t), "verification error",
		},
		{"signed by another key under the same name", byOther, trusted(t), "verification error"},
		{"key not trusted", built, other, "no trusted key"},
		{"no keys", built, map[string]*rsa.PublicKey{}, "no trusted key"},
		{"nil keys", built, nil, "no trusted key"},
		{"nil public key", built, map[string]*rsa.PublicKey{keyName: nil}, "no trusted key"},
		{"unsigned index", unsigned, trusted(t), "not signed"},
		{"signature only", sig, trusted(t), "nothing follows"},
		{"empty", nil, trusted(t), "does not start with a gzip member"},
		{"not gzip", []byte("APKINDEX"), trusted(t), "does not start with a gzip member"},
		{"plain text index", apk.RenderIndex(samplePackages()), trusted(t), "does not start with a gzip member"},
		{
			"signature member with two entries",
			bogus(
				sigEntry{name: name, body: "x"},
				sigEntry{name: ".SIGN.RSA256.other.rsa.pub", body: "x"},
			),
			trusted(t),
			"more than one entry",
		},
		{"signature entry that is not a signature", bogus(sigEntry{name: "APKINDEX", body: "x"}), trusted(t), "not signed"},
		{"unsupported algorithm", bogus(sigEntry{name: ".SIGN.DSA." + keyName, body: "x"}), trusted(t), "unsupported signature"},
		{"no key name", bogus(sigEntry{name: ".SIGN.RSA256.", body: "x"}), trusted(t), "no trusted key"},
		{"key name with a path", bogus(sigEntry{name: ".SIGN.RSA256.../x.rsa.pub", body: "x"}), trusted(t), "no trusted key"},
		{
			"signature that is a symlink",
			bogus(sigEntry{name: name, typ: tar.TypeSymlink, link: "x"}), trusted(t), "malformed signature entry",
		},
		{"empty signature", bogus(sigEntry{name: name}), trusted(t), "verification error"},
		{"signature of 5000 bytes", bogus(sigEntry{name: name, body: strings.Repeat("s", 5000)}), trusted(t), "malformed signature entry"},
		{
			"signature member of 100 KiB",
			bogus(sigEntry{name: name, body: strings.Repeat("s", 100<<10)}), trusted(t), "unreadable or too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VerifyIndex(tt.data, tt.keys)
			if !errors.Is(err, ErrSignature) || got != "" {
				t.Fatalf("got %q, %v, want ErrSignature", got, err)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}
