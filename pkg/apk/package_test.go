// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"archive/tar"
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func parse(b []byte) (*Package, error) { return ParsePackage(bytes.NewReader(b), int64(len(b))) }

func wantPackage(f *fixture, size int64, signed bool) *Package {
	return &Package{
		Name: "hello", Version: "1.2.3-r0", Arch: "x86_64",
		Description: "Prints a greeting", URL: "https://example.org/hello", License: "MIT", Origin: "hello",
		Maintainer: "Test <test@example.org>", Commit: "0123456789abcdef0123456789abcdef01234567",
		BuildTime: 1700000000, InstalledSize: 21,
		Depends:          []string{"so:libc.musl-x86_64.so.1", "busybox>=1.36"},
		Provides:         []string{"cmd:hello=1.2.3-r0"},
		InstallIf:        []string{"docs", "hello=1.2.3-r0"},
		ProviderPriority: 10,
		DataHash:         hexSHA256(f.data),
		Checksum:         sha1Checksum(f.control),
		Size:             size, Signed: signed,
	}
}

func TestLimitConstants(t *testing.T) {
	if maxControlCompressed != 4<<20 || maxControlDecompressed != 8<<20 || maxPKGINFO != 1<<20 ||
		maxControlEntries != 1000 || maxDataDecompressed != 8<<30 || maxDataEntries != 1_000_000 {
		t.Fatal("ParsePackage limits changed")
	}

	if maxIndexBytes != 512<<20 || maxIndexEntries != 2_000_000 || maxIndexLine != 1<<20 {
		t.Fatal("ParseIndex limits changed")
	}
}

func TestParsePackage_valid(t *testing.T) {
	tests := []struct {
		name       string
		sign       string
		controlEnd bool
	}{
		{name: "unsigned, control without end blocks (abuild)"},
		{name: "unsigned, control with end blocks", controlEnd: true},
		{name: "signed SHA-256", sign: "sha256"},
		{name: "signed SHA-1", sign: "sha1"},
		{name: "signed, control with end blocks", sign: "sha256", controlEnd: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.sign)

			if tt.controlEnd {
				f.control = controlMember(t, pkginfoText(f.pkginfo), true)
				if tt.sign != "" {
					f.sig = sigMember(t, tt.sign, f.control)
				}
			}

			b := f.bytes()

			got, err := parse(b)
			if err != nil {
				t.Fatal(err)
			}

			if want := wantPackage(f, int64(len(b)), tt.sign != ""); !reflect.DeepEqual(got, want) {
				t.Fatalf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestParsePackage_variants(t *testing.T) {
	t.Run("control with scripts and triggers", func(t *testing.T) {
		f := newFixture(t, "")
		f.control = controlMember(t, pkginfoText(f.pkginfo), false,
			tarEntry{name: ".pre-install", body: "#!/bin/sh\nexit 0\n"},
			tarEntry{name: ".post-install", body: "#!/bin/sh\nexit 0\n"},
			tarEntry{name: ".trigger", body: "#!/bin/sh\n"},
		)

		got, err := parse(f.bytes())
		if err != nil {
			t.Fatal(err)
		}

		if got.Checksum != sha1Checksum(f.control) {
			t.Fatalf("checksum %s", got.Checksum)
		}
	})

	t.Run("data segment without end blocks", func(t *testing.T) {
		f := newFixture(t, "")
		f.setData(t, "", gz(t, tarBytes(t, []tarEntry{{name: "usr/share/x", body: "x"}}, false)))

		if _, err := parse(f.bytes()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty data tar", func(t *testing.T) {
		f := newFixture(t, "")
		f.setData(t, "", gz(t, tarBytes(t, nil, true)))

		if _, err := parse(f.bytes()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("uppercase datahash is normalised", func(t *testing.T) {
		f := newFixture(t, "")
		f.pkginfo = with(f.pkginfo, "datahash", strings.ToUpper(hexSHA256(f.data)))
		f.rebuild(t, "")

		got, err := parse(f.bytes())
		if err != nil {
			t.Fatal(err)
		}

		if got.DataHash != hexSHA256(f.data) {
			t.Fatalf("datahash %s", got.DataHash)
		}
	})

	t.Run("CRLF PKGINFO", func(t *testing.T) {
		f := newFixture(t, "")
		f.control = controlMember(t, strings.ReplaceAll(pkginfoText(f.pkginfo), "\n", "\r\n"), false)

		if _, err := parse(f.bytes()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("odd but safe entry names and link targets", func(t *testing.T) {
		f := newFixture(t, "")
		f.setData(t, "", gz(t, tarBytes(t, []tarEntry{
			{name: "./", typ: tar.TypeDir},
			{name: "./usr/bin/x", body: "x"},
			{name: "usr/lib/libfoo..so", body: "x"},
			{name: "usr/..hidden/y", body: "y"},
			{name: "usr/bin/abs", typ: tar.TypeSymlink, link: "/etc/passwd"},
			{name: "usr/bin/up", typ: tar.TypeSymlink, link: "../../../../etc/passwd"},
			{name: "usr/bin/hard", typ: tar.TypeLink, link: "../../../etc/shadow"},
		}, true)))

		if _, err := parse(f.bytes()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("long names use PAX records", func(t *testing.T) {
		f := newFixture(t, "")
		f.setData(t, "", gz(t, tarBytes(t, []tarEntry{{name: "usr/share/" + strings.Repeat("d/", 80) + "file", body: "x"}}, true)))

		if _, err := parse(f.bytes()); err != nil {
			t.Fatal(err)
		}
	})
}

// badPackage is a package ParsePackage must refuse for the stated reason.
type badPackage struct {
	name   string
	build  func(t *testing.T) []byte
	limits func(*packageLimits)
	want   string
}

func withData(kind string, entries []tarEntry, end bool) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()

		f := newFixture(t, kind)
		f.setData(t, kind, gz(t, tarBytes(t, entries, end)))

		return f.bytes()
	}
}

func withRawData(raw []byte) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()

		f := newFixture(t, "")
		f.setData(t, "", gz(t, raw))

		return f.bytes()
	}
}

func withPKGINFO(edit func([]kv) []kv) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()

		f := newFixture(t, "")
		f.pkginfo = edit(f.pkginfo)
		f.rebuild(t, "")

		return f.bytes()
	}
}

func badNames() []badPackage {
	var tests []badPackage

	for _, name := range []string{"/etc/passwd", "../evil", "usr/../../evil", "usr/bin/..", "a/b/../../..", ".."} {
		tests = append(tests, badPackage{
			name:  "data entry " + name,
			build: withData("", []tarEntry{{name: "usr/ok", body: "x"}, {name: name, body: "x"}}, true),
			want:  "escapes the install root",
		})
	}

	tests = append(tests,
		badPackage{
			name:  "data entry named by a PAX path record",
			build: withRawData(namedTar('x', "../../pwned")),
			want:  "escapes the install root",
		},
		badPackage{
			name:  "data entry named by a GNU long name",
			build: withRawData(namedTar('L', "usr/../../pwned")),
			want:  "escapes the install root",
		},
	)

	return tests
}

func badStructure() []badPackage {
	return []badPackage{
		{name: "empty file", build: func(*testing.T) []byte { return nil }, want: "empty file"},
		{name: "not gzip", build: func(*testing.T) []byte { return []byte("this is not an apk, only text") }, want: "not gzip"},
		{name: "gzip of something that is not a tar", build: func(t *testing.T) []byte {
			t.Helper()
			return gz(t, []byte("hello"))
		}, want: "no tar entry"},
		{name: "gzip of nothing", build: func(t *testing.T) []byte {
			t.Helper()
			return gz(t, nil)
		}, want: "no tar entry"},
		{
			name: "control segment only",
			build: func(t *testing.T) []byte {
				t.Helper()
				return newFixture(t, "").control
			},
			want: "no data segment",
		},
		{
			name: "signature and control only",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "sha256")

				return concat(f.sig, f.control)
			},
			want: "no data segment",
		},
		{
			name: "signature only",
			build: func(t *testing.T) []byte {
				t.Helper()
				return newFixture(t, "sha256").sig
			},
			want: "no control segment",
		},
		{
			name: "data segment first",
			build: func(t *testing.T) []byte {
				t.Helper()
				return defaultData(t)
			},
			want: "starts with",
		},
		{
			name: "control segment starting with a script",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.control = gz(
					t,
					tarBytes(t, []tarEntry{{name: ".pre-install", body: "x"}, {name: ".PKGINFO", body: pkginfoText(f.pkginfo)}}, false),
				)

				return f.bytes()
			},
			want: "starts with",
		},
		{
			name: "signature segment with a foreign entry",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				sig := gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA.k.rsa.pub", body: "x"}, {name: ".PKGINFO", body: "x"}}, false))

				return concat(sig, f.control, f.data)
			},
			want: "is not a signature",
		},
		{
			name: "two signature segments",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "sha256")

				return concat(f.sig, f.sig, f.control, f.data)
			},
			want: "starts with",
		},
		{
			name: "duplicate .PKGINFO",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.control = controlMember(t, pkginfoText(f.pkginfo), false, tarEntry{name: ".PKGINFO", body: "x"})

				return f.bytes()
			},
			want: "more than one",
		},
		{
			name: ".PKGINFO that is a symlink",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.control = gz(t, tarBytes(t, []tarEntry{{name: ".PKGINFO", typ: tar.TypeSymlink, link: "/etc/passwd"}}, false))

				return f.bytes()
			},
			want: "not a regular file",
		},
	}
}

func badData() []badPackage {
	return []badPackage{
		{
			name: "datahash of another segment",
			build: withPKGINFO(func(l []kv) []kv {
				return with(l, "datahash", strings.Repeat("ab", 32))
			}),
			want: "datahash does not match",
		},
		{name: "datahash missing", build: withPKGINFO(func(l []kv) []kv { return without(l, "datahash") }), want: "no datahash"},
		{name: "datahash empty", build: withPKGINFO(func(l []kv) []kv { return with(l, "datahash", "") }), want: "datahash"},
		{
			name:  "datahash not hex",
			build: withPKGINFO(func(l []kv) []kv { return with(l, "datahash", strings.Repeat("zz", 32)) }),
			want:  "datahash",
		},
		{name: "datahash too short", build: withPKGINFO(func(l []kv) []kv { return with(l, "datahash", "abcd") }), want: "datahash"},
		{
			name: "garbage after the data segment",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")

				return concat(f.bytes(), []byte("trailing garbage"))
			},
			want: "datahash does not match",
		},
		{
			name: "garbage after the data segment covered by the datahash",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.setData(t, "", concat(f.data, []byte("trailing garbage")))

				return f.bytes()
			},
			want: "bytes after the data segment",
		},
		{
			name: "a fourth member covered by the datahash",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "sha256")
				f.setData(t, "sha256", concat(f.data, defaultData(t)))

				return f.bytes()
			},
			want: "bytes after the data segment",
		},
		{
			name: "data segment truncated, datahash updated",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.setData(t, "", f.data[:len(f.data)-12])

				return f.bytes()
			},
			want: "unexpected EOF",
		},
		{
			name: "data segment CRC corrupt, datahash updated",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				data := append([]byte(nil), f.data...)
				data[len(data)-8] ^= 0xff
				f.setData(t, "", data)

				return f.bytes()
			},
			want: "invalid checksum",
		},
		{
			name: "data segment that is not gzip",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.setData(t, "", []byte("plain bytes, not a gzip member"))

				return f.bytes()
			},
			want: "data segment is not gzip",
		},
		{
			name: "control segment CRC corrupt",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.control[len(f.control)-8] ^= 0xff

				return f.bytes()
			},
			want: "invalid checksum",
		},
		{
			name: "data tar with a corrupt header",
			build: func(t *testing.T) []byte {
				t.Helper()

				raw := tarBytes(t, []tarEntry{{name: "usr/x", body: "x"}}, true)
				raw[148] ^= 0xff // header checksum field
				f := newFixture(t, "")
				f.setData(t, "", gz(t, raw))

				return f.bytes()
			},
			want: "data segment:",
		},
	}
}

func badLimits() []badPackage {
	lots := make([]tarEntry, 10)
	for i := range lots {
		lots[i] = tarEntry{name: "usr/f" + string(rune('a'+i)), body: "x"}
	}

	return []badPackage{
		{
			name:   "too many data entries",
			build:  withData("", lots, true),
			limits: func(l *packageLimits) { l.dataEntries = 5 },
			want:   "more than 5 entries",
		},
		{
			name:   "data decompressed over the cap",
			build:  withData("", []tarEntry{{name: "usr/big", body: strings.Repeat("A", 100_000)}}, true),
			limits: func(l *packageLimits) { l.dataDecompressed = 10_000 },
			want:   "exceeds the limit",
		},
		{
			name:   "data decompressed over the cap on the tar end blocks",
			build:  withData("", []tarEntry{{name: "usr/x", body: "x"}}, true),
			limits: func(l *packageLimits) { l.dataDecompressed = 1024 + 512 },
			want:   "exceeds the limit",
		},
		{
			name: "control decompressed over the cap",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.control = controlMember(
					t,
					pkginfoText(f.pkginfo),
					false,
					tarEntry{name: ".post-install", body: strings.Repeat("x", 100_000)},
				)

				return f.bytes()
			},
			limits: func(l *packageLimits) { l.controlDecompressed = 20_000 },
			want:   "decompressed bytes",
		},
		{
			name: "control compressed over the cap",
			build: func(t *testing.T) []byte {
				t.Helper()

				return newFixture(t, "").bytes()
			},
			limits: func(l *packageLimits) { l.controlCompressed = 64 },
			want:   "compressed bytes",
		},
		{
			name: "signature compressed over the cap",
			build: func(t *testing.T) []byte {
				t.Helper()
				return newFixture(t, "sha256").bytes()
			},
			limits: func(l *packageLimits) { l.controlCompressed = 64 },
			want:   "compressed bytes",
		},
		{
			name: ".PKGINFO over the cap",
			build: func(t *testing.T) []byte {
				t.Helper()

				return newFixture(t, "").bytes()
			},
			limits: func(l *packageLimits) { l.pkginfo = 100 },
			want:   "exceeds 100 bytes",
		},
		{
			name: "too many control entries",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				f.control = controlMember(t, pkginfoText(f.pkginfo), false, lots...)

				return f.bytes()
			},
			limits: func(l *packageLimits) { l.controlEntries = 4 },
			want:   "more than 4 entries",
		},
		{
			name: "too many signature entries",
			build: func(t *testing.T) []byte {
				t.Helper()

				f := newFixture(t, "")
				sig := gz(
					t,
					tarBytes(
						t,
						[]tarEntry{{name: ".SIGN.a", body: "x"}, {name: ".SIGN.b", body: "x"}, {name: ".SIGN.c", body: "x"}},
						false,
					),
				)

				return concat(sig, f.control, f.data)
			},
			limits: func(l *packageLimits) { l.controlEntries = 2 },
			want:   "more than 2 entries",
		},
	}
}

func TestParsePackage_invalid(t *testing.T) {
	var tests []badPackage

	tests = append(tests, badStructure()...)
	tests = append(tests, badData()...)
	tests = append(tests, badNames()...)
	tests = append(tests, badLimits()...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.build(t)
			lim := defaultPackageLimits()

			if tt.limits != nil {
				tt.limits(&lim)
			}

			got, err := parsePackage(bytes.NewReader(b), int64(len(b)), lim)
			if err == nil {
				t.Fatalf("accepted: %+v", got)
			}

			if !errors.Is(err, ErrInvalidPackage) {
				t.Fatalf("error %q does not wrap ErrInvalidPackage", err)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// TestParsePackage_truncated cuts a signed package at every length: none of
// the prefixes may be accepted.
func TestParsePackage_truncated(t *testing.T) {
	b := newFixture(t, "sha256").bytes()

	for n := range len(b) {
		if _, err := parse(b[:n]); !errors.Is(err, ErrInvalidPackage) {
			t.Fatalf("prefix of %d/%d bytes: %v", n, len(b), err)
		}
	}
}

// TestParsePackage_bitFlips changes every byte of a signed package in turn:
// ParsePackage may accept or refuse the result but must always fail with
// ErrInvalidPackage and never panic.
func TestParsePackage_bitFlips(t *testing.T) {
	b := newFixture(t, "sha256").bytes()

	for i := range b {
		mutated := append([]byte(nil), b...)
		mutated[i] ^= 0x41

		if _, err := parse(mutated); err != nil && !errors.Is(err, ErrInvalidPackage) {
			t.Fatalf("byte %d: %v", i, err)
		}
	}
}

func TestParsePackage_invalidFields(t *testing.T) {
	tests := []struct {
		name string
		edit func([]kv) []kv
		want string
	}{
		{"bad name", func(l []kv) []kv { return with(l, "pkgname", "../etc") }, "pkgname"},
		{"bad version", func(l []kv) []kv { return with(l, "pkgver", "v1.0") }, "pkgver"},
		{"bad arch", func(l []kv) []kv { return with(l, "arch", "x86-64") }, "arch"},
		{"missing name", func(l []kv) []kv { return without(l, "pkgname") }, "no pkgname"},
		{"duplicate name", func(l []kv) []kv { return plus(l, "pkgname", "other") }, "duplicate pkgname"},
		{"control character", func(l []kv) []kv { return with(l, "pkgdesc", "a\x01b") }, "pkgdesc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(withPKGINFO(tt.edit)(t))
			if !errors.Is(err, ErrInvalidPackage) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an ErrInvalidPackage mentioning %q", err, tt.want)
			}
		})
	}
}
