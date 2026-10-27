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

// samplePackages returns index entries: a full one, a minimal one and one
// shaped like Alpine's own.
func samplePackages() []Package {
	return []Package{
		{
			Name: "hello", Version: "1.2.3-r0", Arch: "x86_64", Checksum: sha1Checksum([]byte("hello")),
			Size: 1234, InstalledSize: 21, Description: "Prints a greeting", URL: "https://example.org/hello", License: "MIT",
			Origin: "hello", Maintainer: "Test <test@example.org>", BuildTime: 1700000000,
			Commit:  "0123456789abcdef0123456789abcdef01234567",
			Depends: []string{"so:libc.musl-x86_64.so.1", "busybox>=1.36"}, Provides: []string{"cmd:hello=1.2.3-r0"},
			InstallIf: []string{"docs", "hello=1.2.3-r0"}, ProviderPriority: 10,
		},
		{Name: "min", Version: "1", Arch: "noarch", Checksum: sha1Checksum([]byte("min"))},
		{
			Name: "py3-sphinx", Version: "3.2.1-r0", Arch: "x86_64", Checksum: "Q1ednhuhRAAB2bHV017RsxJqKY0Tc=",
			Size: 3468010, InstalledSize: 15851520, Description: "Python Documentation Generator",
			URL: "http://sphinx.pocoo.org/", License: "BSD-2-Clause", Origin: "py3-sphinx",
			Maintainer: "Fabian Affolter <fabian@affolter-engineering.ch>", BuildTime: 1597611676,
			Commit:  "2b4dcfc909482ab4765fe5d02b642abd686114f6",
			Depends: []string{"py3-babel", "py3-docutils", "py3-jinja2"}, Provides: []string{"cmd:sphinx-apidoc", "cmd:sphinx-build"},
		},
	}
}

const goldenIndex = `C:Q1qvTGHdzF6KLavt4PO0gs2a6pQ00=
P:hello
V:1.2.3-r0
A:x86_64
S:1234
I:21
T:Prints a greeting
U:https://example.org/hello
L:MIT
o:hello
m:Test <test@example.org>
t:1700000000
c:0123456789abcdef0123456789abcdef01234567
D:so:libc.musl-x86_64.so.1 busybox>=1.36
p:cmd:hello=1.2.3-r0
i:docs hello=1.2.3-r0
k:10

C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=
P:min
V:1
A:noarch

`

func TestRenderIndex(t *testing.T) {
	t.Run("golden", func(t *testing.T) {
		got := string(RenderIndex(samplePackages()[:2]))
		if got != goldenIndex {
			t.Fatalf("got\n%s\nwant\n%s", got, goldenIndex)
		}
	})

	t.Run("no packages", func(t *testing.T) {
		if got := RenderIndex(nil); len(got) != 0 {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("control characters cannot start a line", func(t *testing.T) {
		p := samplePackages()[1]
		p.Description = "a\nP:evil\r\nC:x\x00\ttab\u0085next"
		p.Depends = []string{"a\nb"}

		got := string(RenderIndex([]Package{p}))
		if strings.Count(got, "\n") != strings.Count(goldenIndexMinimal, "\n")+2 {
			t.Fatalf("unexpected line count in %q", got)
		}

		if !strings.Contains(got, "T:a P:evil  C:x  tab next\n") {
			t.Fatalf("description not flattened: %q", got)
		}
	})
}

const goldenIndexMinimal = "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:noarch\n\n"

func TestParseIndex_roundTrip(t *testing.T) {
	want := samplePackages()

	got, err := ParseIndex(bytes.NewReader(RenderIndex(want)))
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseIndex_golden(t *testing.T) {
	got, err := ParseIndex(strings.NewReader(goldenIndex))
	if err != nil {
		t.Fatal(err)
	}

	if want := samplePackages()[:2]; !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// indexMember is the gzip member abuild writes for the index.
func indexMember(t *testing.T, text string, end bool, extra ...tarEntry) []byte {
	t.Helper()

	entries := append([]tarEntry{{name: "DESCRIPTION", body: "test"}, {name: "APKINDEX", body: text}}, extra...)

	return gz(t, tarBytes(t, entries, end))
}

func TestParseIndex_forms(t *testing.T) {
	text := string(RenderIndex(samplePackages()))
	want := samplePackages()

	tests := []struct {
		name  string
		input func(t *testing.T) []byte
	}{
		{"plain text", func(*testing.T) []byte { return []byte(text) }},
		{"index member only", func(t *testing.T) []byte {
			t.Helper()
			return indexMember(t, text, true)
		}},
		{"index member without end blocks", func(t *testing.T) []byte {
			t.Helper()
			return indexMember(t, text, false)
		}},
		{"signed", func(t *testing.T) []byte {
			t.Helper()

			sig := gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.k.rsa.pub", body: "signature"}}, false))

			return concat(sig, indexMember(t, text, true))
		}},
		{"signature after the index", func(t *testing.T) []byte {
			t.Helper()

			sig := gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA.k.rsa.pub", body: "signature"}}, true))

			return concat(indexMember(t, text, true), sig)
		}},
		{"APKINDEX before DESCRIPTION", func(t *testing.T) []byte {
			t.Helper()

			return gz(t, tarBytes(t, []tarEntry{{name: "APKINDEX", body: text}, {name: "DESCRIPTION", body: "x"}}, true))
		}},
		{"other entries are skipped", func(t *testing.T) []byte {
			t.Helper()

			return indexMember(t, text, true, tarEntry{name: "README", body: "hi"})
		}},
		{"no trailing newline", func(*testing.T) []byte { return []byte(strings.TrimRight(text, "\n")) }},
		{"extra blank lines", func(*testing.T) []byte {
			return []byte("\n\n" + strings.ReplaceAll(text, "\n\n", "\n\n\n\n") + "\n")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIndex(bytes.NewReader(tt.input(t)))
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestParseIndex_empty(t *testing.T) {
	for name, in := range map[string][]byte{"nothing": nil, "blank lines": []byte("\n\n\n")} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseIndex(bytes.NewReader(in))
			if err != nil || len(got) != 0 {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}

	t.Run("an archive with an empty index", func(t *testing.T) {
		got, err := ParseIndex(bytes.NewReader(indexMember(t, "", true)))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
}

func TestParseIndex_leniency(t *testing.T) {
	t.Run("unknown fields are ignored", func(t *testing.T) {
		text := "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nX:future\nz:other\nV:1\nA:noarch\nr:5\n\n"

		got, err := ParseIndex(strings.NewReader(text))
		if err != nil || len(got) != 1 || got[0].Name != "min" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("unknown fields may repeat", func(t *testing.T) {
		text := "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:noarch\nX:a\nX:b\n\n"

		if _, err := ParseIndex(strings.NewReader(text)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("control characters in free text become spaces", func(t *testing.T) {
		text := "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:noarch\nT:Python\tDocumentation\x01Generator\nU:http://x/\x7f\n\n"

		got, err := ParseIndex(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}

		if got[0].Description != "Python Documentation Generator" || got[0].URL != "http://x/ " {
			t.Fatalf("%+v", got[0])
		}
	})

	t.Run("tabs separate list entries", func(t *testing.T) {
		text := "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:noarch\nD:a\tb  c\n\n"

		got, err := ParseIndex(strings.NewReader(text))
		if err != nil || !reflect.DeepEqual(got[0].Depends, []string{"a", "b", "c"}) {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

const minimalStanza = "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:noarch\n"

func TestParseIndex_invalid(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"missing C", "P:min\nV:1\nA:noarch\n", "missing field C"},
		{"missing P", "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nV:1\nA:noarch\n", "missing field P"},
		{"missing V", "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nA:noarch\n", "missing field V"},
		{"missing A", "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\n", "missing field A"},
		{"second stanza incomplete", minimalStanza + "\nP:other\nV:1\n", "entry 2"},
		{"line without a colon", minimalStanza + "garbage\n", "not of the form"},
		{"line of one byte", minimalStanza + "x\n", "not of the form"},
		{"key longer than one letter", minimalStanza + "PP:x\n", "not of the form"},
		{"duplicate P", minimalStanza + "P:other\n", "appears twice"},
		{"duplicate C", minimalStanza + "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\n", "appears twice"},
		{"duplicate D", minimalStanza + "D:a\nD:b\n", "appears twice"},
		{"duplicate T", minimalStanza + "T:a\nT:b\n", "appears twice"},
		{"bad checksum", "C:Q1short\nP:min\nV:1\nA:noarch\n", "checksum"},
		{"checksum without Q1", "C:ZZtsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:noarch\n", "checksum"},
		{"bad name", "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:../min\nV:1\nA:noarch\n", "pkgname"},
		{"bad version", "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:x\nA:noarch\n", "pkgver"},
		{"bad arch", "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\nP:min\nV:1\nA:no/arch\n", "arch"},
		{"CR at the end of a line", "C:Q1tsk11PPHsiD6A4YTofnBtWslWoY=\r\nP:min\r\nV:1\r\nA:noarch\r\n", "invalid field"},
		{"bad size", minimalStanza + "S:12x\n", "not a valid number"},
		{"negative installed size", minimalStanza + "I:-1\n", "not a valid number"},
		{"empty time", minimalStanza + "t:\n", "not a valid number"},
		{"overflowing size", minimalStanza + "S:99999999999999999999\n", "not a valid number"},
		{"huge priority", minimalStanza + "k:2147483648\n", "not a valid number"},
		{"long description", minimalStanza + "T:" + strings.Repeat("d", 1025) + "\n", "pkgdesc"},
		{"long dependency", minimalStanza + "D:" + strings.Repeat("d", 257) + "\n", "depend"},
		{"control character in a list", minimalStanza + "p:a\x01b\n", "provides"},
		{"invalid UTF-8 in a list", minimalStanza + "i:a\xffb\n", "install_if"},
		{"too many dependencies", minimalStanza + "D:" + strings.Repeat("d ", maxTokens+1) + "\n", "depend"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIndex(strings.NewReader(tt.text))
			if err == nil {
				t.Fatalf("accepted: %+v", got)
			}

			if !errors.Is(err, ErrInvalidIndex) {
				t.Fatalf("error %q does not wrap ErrInvalidIndex", err)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestParseIndex_invalidArchives(t *testing.T) {
	text := string(RenderIndex(samplePackages()))
	sig := gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.k.rsa.pub", body: "signature"}}, false))

	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{"signature only", sig, "no APKINDEX entry"},
		{"member without an APKINDEX", gz(t, tarBytes(t, []tarEntry{{name: "DESCRIPTION", body: "x"}}, true)), "holds no APKINDEX"},
		{"two index members", concat(indexMember(t, text, true), indexMember(t, text, true)), "more than one APKINDEX"},
		{
			"two index entries",
			gz(t, tarBytes(t, []tarEntry{{name: "APKINDEX", body: text}, {name: "APKINDEX", body: text}}, true)),
			"more than one APKINDEX entry",
		},
		{"garbage after the member", concat(indexMember(t, text, true), []byte("garbage")), "gzip member"},
		{"zero bytes after the member", concat(indexMember(t, text, true), make([]byte, 16)), "gzip member"},
		{"truncated member", indexMember(t, text, true)[:40], "unexpected EOF"},
		{"truncated tar", gz(t, tarBytes(t, []tarEntry{{name: "APKINDEX", body: text}}, true)[:600]), "unexpected EOF"},
		{"gzip of something that is not a tar", gz(t, []byte(strings.Repeat("not a tar header ", 64))), "tar"},
		{"corrupt gzip checksum", func() []byte {
			b := indexMember(t, text, true)
			b[len(b)-8] ^= 0xff

			return b
		}(), "invalid checksum"},
		{
			"APKINDEX that is a symlink",
			gz(t, tarBytes(t, []tarEntry{{name: "APKINDEX", typ: tar.TypeSymlink, link: "/etc/passwd"}}, true)),
			"holds no APKINDEX",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIndex(bytes.NewReader(tt.input))
			if err == nil {
				t.Fatalf("accepted: %d packages", len(got))
			}

			if !errors.Is(err, ErrInvalidIndex) {
				t.Fatalf("error %q does not wrap ErrInvalidIndex", err)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func sigMemberFor(tb testing.TB) []byte {
	tb.Helper()

	return gz(tb, tarBytes(tb, []tarEntry{{name: ".SIGN.RSA256.k.rsa.pub", body: "signature"}}, false))
}

func TestParseIndex_limits(t *testing.T) {
	text := string(RenderIndex(samplePackages()))

	tests := []struct {
		name   string
		input  []byte
		limits func(*indexLimits)
		want   string
	}{
		{
			name:   "entries in plain text",
			input:  []byte(text),
			limits: func(l *indexLimits) { l.entries = 2 },
			want:   "more than 2 entries",
		},
		{
			name:   "entries in an archive",
			input:  indexMember(t, text, true),
			limits: func(l *indexLimits) { l.entries = 1 },
			want:   "more than 1 entries",
		},
		{
			name:   "line length",
			input:  []byte(minimalStanza + "T:" + strings.Repeat("d", 200) + "\n"),
			limits: func(l *indexLimits) { l.line = 100 },
			want:   "longer than 100 bytes",
		},
		{
			name:   "line length without a newline",
			input:  []byte(minimalStanza + "T:" + strings.Repeat("d", 5000)),
			limits: func(l *indexLimits) { l.line = 100 },
			want:   "longer than 100 bytes",
		},
		{
			name:   "decompressed bytes of plain text",
			input:  []byte(text),
			limits: func(l *indexLimits) { l.bytes = 100 },
			want:   "exceeds",
		},
		{
			name:   "decompressed bytes of an archive",
			input:  indexMember(t, text, true),
			limits: func(l *indexLimits) { l.bytes = 1000 },
			want:   "exceeds",
		},
		{
			name: "decompressed bytes across members",
			input: concat(
				gz(t, tarBytes(t, []tarEntry{{name: ".SIGN.RSA256.k", body: strings.Repeat("s", 600)}}, false)),
				indexMember(t, text, true),
			),
			limits: func(l *indexLimits) {
				l.bytes = 600 + 512 + 512 + 600
			},
			want: "exceeds",
		},
		{
			name:   "members",
			input:  concat(sigMemberFor(t), sigMemberFor(t), indexMember(t, text, true)),
			limits: func(l *indexLimits) { l.members = 2 },
			want:   "more than 2 gzip members",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := defaultIndexLimits()
			tt.limits(&lim)

			got, err := parseIndex(bytes.NewReader(tt.input), lim)
			if err == nil {
				t.Fatalf("accepted: %d packages", len(got))
			}

			if !errors.Is(err, ErrInvalidIndex) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want ErrInvalidIndex mentioning %q", err, tt.want)
			}
		})
	}
}

// TestParseIndex_bomb feeds a gzip member that expands to far more than the
// cap: the parse must stop at the cap instead of inflating it all.
func TestParseIndex_bomb(t *testing.T) {
	var body strings.Builder

	body.WriteString(minimalStanza)

	for range 2000 {
		body.WriteString("X:" + strings.Repeat("a", 500) + "\n")
	}

	lim := defaultIndexLimits()
	lim.bytes = 64 << 10

	_, err := parseIndex(bytes.NewReader(indexMember(t, body.String(), true)), lim)
	if !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("got %v", err)
	}
}
