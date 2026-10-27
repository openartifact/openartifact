// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"archive/tar"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// sameIndex reports whether packages re-render to the same index.
func sameIndex(t *testing.T, pkgs []Package) {
	t.Helper()

	rendered := RenderIndex(pkgs)

	again, err := ParseIndex(bytes.NewReader(rendered))
	if err != nil {
		t.Fatalf("rendered index does not parse: %v\n%s", err, rendered)
	}

	if len(again) != len(pkgs) {
		t.Fatalf("rendered %d packages, parsed %d\n%s", len(pkgs), len(again), rendered)
	}

	if !bytes.Equal(RenderIndex(again), rendered) {
		t.Fatalf("index changed by a render/parse round trip:\n%s", rendered)
	}
}

func FuzzParsePackage(f *testing.F) {
	for _, kind := range []string{"", "sha256", "sha1"} {
		fx := newFixture(f, kind)
		f.Add(fx.bytes())
	}

	long := newFixture(f, "")
	long.setData(f, "", gz(f, tarBytes(f, []tarEntry{
		{name: "usr/", typ: tar.TypeDir}, {name: "usr/bin/x", body: "x"}, {name: "usr/bin/y", typ: tar.TypeSymlink, link: "x"},
	}, false)))
	f.Add(long.bytes())

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParsePackage(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			if !errors.Is(err, ErrInvalidPackage) {
				t.Fatalf("error does not wrap ErrInvalidPackage: %v", err)
			}

			return
		}

		if err := p.validate(); err != nil {
			t.Fatalf("accepted package fails validation: %v", err)
		}

		if p.Size != int64(len(data)) || p.DataHash == "" || p.Checksum == "" {
			t.Fatalf("incomplete package: %+v", p)
		}

		if name, version, ok := ParseFileName(p.FileName()); !ok || name != p.Name || version != p.Version {
			t.Fatalf("file name %q does not round trip", p.FileName())
		}

		sameIndex(t, []Package{*p})
	})
}

func FuzzParseIndex(f *testing.F) {
	text := RenderIndex(samplePackages())
	f.Add(text)
	f.Add(gz(f, tarBytes(f, []tarEntry{{name: "DESCRIPTION", body: "d"}, {name: "APKINDEX", body: string(text)}}, true)))
	f.Add(concat(sigMemberFor(f), gz(f, tarBytes(f, []tarEntry{{name: "APKINDEX", body: string(text)}}, false))))
	f.Add([]byte("C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:noarch\nT:a\tb\nD:x y\n\nP:second\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		pkgs, err := ParseIndex(bytes.NewReader(data))
		if err != nil {
			if !errors.Is(err, ErrInvalidIndex) {
				t.Fatalf("error does not wrap ErrInvalidIndex: %v", err)
			}

			return
		}

		for i := range pkgs {
			if err := pkgs[i].validate(); err != nil {
				t.Fatalf("entry %d accepted but invalid: %v", i, err)
			}
		}

		sameIndex(t, pkgs)
	})
}

func FuzzParseFileName(f *testing.F) {
	for _, s := range []string{"busybox-1.36.1-r0.apk", "py3-sphinx-3.2.1-r0.apk", "lib-2-1.0.apk", "a-1.apk", ".apk", "-1.apk", "a-1-r0-r1.apk"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		name, version, ok := ParseFileName(s)
		if !ok {
			if name != "" || version != "" {
				t.Fatalf("(%q, %q) returned with ok=false", name, version)
			}

			return
		}

		if !validName(name) || !validVersion(version) {
			t.Fatalf("ParseFileName(%q) = (%q, %q), not valid", s, name, version)
		}

		if got := (&Package{Name: name, Version: version}).FileName(); got != s {
			t.Fatalf("ParseFileName(%q) = (%q, %q), which is the file %q", s, name, version, got)
		}
	})
}

func FuzzPKGINFO(f *testing.F) {
	f.Add([]byte(pkginfoText(basePKGINFO(strings.Repeat("0", 64)))))
	f.Add([]byte("pkgname = a\npkgver = 1\narch = noarch\ndatahash = " + strings.Repeat("a", 64) + "\n"))
	f.Add([]byte("pkgname = a\npkgver = 1\narch = noarch\ndatahash = " + strings.Repeat("a", 64) + "\npkgdesc = x\r\nC:Q1\n"))
	f.Add([]byte("# comment\n\nunknown = 1\ndepend = a b\ndepend = c\nprovides = d=1\ninstall_if = e\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := parsePKGINFO(data)
		if err != nil {
			if !errors.Is(err, ErrInvalidPackage) {
				t.Fatalf("error does not wrap ErrInvalidPackage: %v", err)
			}

			return
		}

		p.Checksum = sha1Checksum(nil)

		if p.validate() != nil {
			return
		}

		// A package that validates renders as exactly one stanza whatever the
		// text said.
		sameIndex(t, []Package{*p})

		got, err := ParseIndex(bytes.NewReader(RenderIndex([]Package{*p})))
		if err != nil || len(got) != 1 || got[0].Name != p.Name || got[0].Version != p.Version || got[0].Arch != p.Arch {
			t.Fatalf("rendered package changed: %+v, %v", got, err)
		}
	})
}
