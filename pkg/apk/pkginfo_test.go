// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// parseInfo parses and validates PKGINFO text the way ParsePackage does.
func parseInfo(text string) (*Package, error) {
	p, err := parsePKGINFO([]byte(text))
	if err != nil {
		return nil, err
	}

	p.Checksum = sha1Checksum(nil)

	if err := p.validate(); err != nil {
		return nil, bad("%v", err)
	}

	return p, nil
}

func info(edit func([]kv) []kv) string {
	return pkginfoText(edit(basePKGINFO(strings.Repeat("0", 64))))
}

func TestParsePKGINFO_keys(t *testing.T) {
	p, err := parseInfo(info(func(l []kv) []kv { return l }))
	if err != nil {
		t.Fatal(err)
	}

	want := Package{
		Name: "hello", Version: "1.2.3-r0", Arch: "x86_64", DataHash: strings.Repeat("0", 64), Checksum: sha1Checksum(nil),
		Description: "Prints a greeting", URL: "https://example.org/hello", License: "MIT", Origin: "hello",
		Maintainer: "Test <test@example.org>", Commit: "0123456789abcdef0123456789abcdef01234567",
		BuildTime: 1700000000, InstalledSize: 21, ProviderPriority: 10,
		Depends:   []string{"so:libc.musl-x86_64.so.1", "busybox>=1.36"},
		Provides:  []string{"cmd:hello=1.2.3-r0"},
		InstallIf: []string{"docs", "hello=1.2.3-r0"},
	}

	if !reflect.DeepEqual(*p, want) {
		t.Fatalf("got  %+v\nwant %+v", *p, want)
	}
}

func TestParsePKGINFO_valid(t *testing.T) {
	tests := []struct {
		name  string
		edit  func([]kv) []kv
		check func(*testing.T, *Package)
	}{
		{
			name: "only the required keys",
			edit: func([]kv) []kv {
				return []kv{{"pkgname", "a"}, {"pkgver", "1"}, {"arch", "noarch"}, {"datahash", strings.Repeat("a", 64)}}
			},
		},
		{
			name: "unknown keys, also repeated, are ignored",
			edit: func(l []kv) []kv {
				return plus(plus(plus(l, "replaces", "a"), "replaces", "b"), "triggers", "/usr/share")
			},
		},
		{
			name: "repeated depend and provides accumulate",
			edit: func(l []kv) []kv { return plus(plus(l, "depend", "x y"), "provides", "z") },
			check: func(t *testing.T, p *Package) {
				t.Helper()

				if len(p.Depends) != 4 || len(p.Provides) != 2 || p.Depends[3] != "y" {
					t.Fatalf("%v %v", p.Depends, p.Provides)
				}
			},
		},
		{
			name: "upper case in the name",
			edit: func(l []kv) []kv { return with(l, "pkgname", "Hello_World+1.x") },
		},
		{
			name: "empty optional values",
			edit: func(l []kv) []kv { return with(with(with(l, "pkgdesc", ""), "url", ""), "license", "") },
			check: func(t *testing.T, p *Package) {
				t.Helper()

				if p.Description != "" || p.URL != "" || p.License != "" {
					t.Fatalf("%+v", p)
				}
			},
		},
		{
			name: "UTF-8 text",
			edit: func(l []kv) []kv { return with(l, "pkgdesc", "Grüße 你好 ✓") },
			check: func(t *testing.T, p *Package) {
				t.Helper()

				if p.Description != "Grüße 你好 ✓" {
					t.Fatal(p.Description)
				}
			},
		},
		{
			name: "values that contain an equals sign",
			edit: func(l []kv) []kv { return with(l, "url", "https://example.org/?a=b&c=d") },
			check: func(t *testing.T, p *Package) {
				t.Helper()

				if p.URL != "https://example.org/?a=b&c=d" {
					t.Fatal(p.URL)
				}
			},
		},
		{
			name: "description of exactly 1024 bytes",
			edit: func(l []kv) []kv { return with(l, "pkgdesc", strings.Repeat("d", 1024)) },
		},
		{
			name: "token of exactly 256 bytes",
			edit: func(l []kv) []kv { return plus(l, "depend", strings.Repeat("t", 256)) },
		},
		{
			name: "name of exactly 128 bytes",
			edit: func(l []kv) []kv { return with(l, "pkgname", strings.Repeat("n", 128)) },
		},
		{
			name: "maximum provider priority",
			edit: func(l []kv) []kv { return with(l, "provider_priority", "2147483647") },
		},
		{
			name: "zero numbers",
			edit: func(l []kv) []kv { return with(with(l, "size", "0"), "builddate", "0") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseInfo(info(tt.edit))
			if err != nil {
				t.Fatal(err)
			}

			if tt.check != nil {
				tt.check(t, p)
			}
		})
	}
}

func TestParsePKGINFO_tokenCount(t *testing.T) {
	tokens := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("p%d", i)
		}

		return strings.Join(parts, " ")
	}

	if _, err := parseInfo(info(func(l []kv) []kv { return with(l, "depend", tokens(maxTokens-1)) })); err != nil {
		t.Fatalf("%d tokens in total: %v", maxTokens, err)
	}

	if _, err := parseInfo(info(func(l []kv) []kv { return with(l, "depend", tokens(maxTokens)) })); err == nil {
		t.Fatalf("%d depends plus one accepted", maxTokens)
	}
}

func TestParsePKGINFO_invalid(t *testing.T) {
	tests := []struct {
		name string
		edit func([]kv) []kv
		want string
	}{
		{"missing pkgname", func(l []kv) []kv { return without(l, "pkgname") }, "no pkgname"},
		{"missing pkgver", func(l []kv) []kv { return without(l, "pkgver") }, "no pkgver"},
		{"missing arch", func(l []kv) []kv { return without(l, "arch") }, "no arch"},
		{"missing datahash", func(l []kv) []kv { return without(l, "datahash") }, "no datahash"},
		{"duplicate pkgname", func(l []kv) []kv { return plus(l, "pkgname", "other") }, "duplicate pkgname"},
		{"duplicate pkgver", func(l []kv) []kv { return plus(l, "pkgver", "2") }, "duplicate pkgver"},
		{"duplicate arch", func(l []kv) []kv { return plus(l, "arch", "noarch") }, "duplicate arch"},
		{"duplicate datahash", func(l []kv) []kv { return plus(l, "datahash", strings.Repeat("1", 64)) }, "duplicate datahash"},
		{"duplicate pkgdesc", func(l []kv) []kv { return plus(l, "pkgdesc", "other") }, "duplicate pkgdesc"},
		{"duplicate url", func(l []kv) []kv { return plus(l, "url", "other") }, "duplicate url"},
		{"duplicate license", func(l []kv) []kv { return plus(l, "license", "other") }, "duplicate license"},
		{"duplicate origin", func(l []kv) []kv { return plus(l, "origin", "other") }, "duplicate origin"},
		{"duplicate maintainer", func(l []kv) []kv { return plus(l, "maintainer", "other") }, "duplicate maintainer"},
		{"duplicate commit", func(l []kv) []kv { return plus(l, "commit", "other") }, "duplicate commit"},
		{"duplicate size", func(l []kv) []kv { return plus(l, "size", "1") }, "duplicate size"},
		{"duplicate builddate", func(l []kv) []kv { return plus(l, "builddate", "1") }, "duplicate builddate"},
		{"duplicate install_if", func(l []kv) []kv { return plus(l, "install_if", "a") }, "install_if"},
		{"duplicate provider_priority", func(l []kv) []kv { return plus(l, "provider_priority", "1") }, "duplicate provider_priority"},
		{"empty depend", func(l []kv) []kv { return plus(l, "depend", "") }, "depend"},
		{"empty provides", func(l []kv) []kv { return plus(l, "provides", "  ") }, "provides"},
		{"empty install_if", func(l []kv) []kv { return with(l, "install_if", "") }, "install_if"},
		{"long description", func(l []kv) []kv { return with(l, "pkgdesc", strings.Repeat("d", 1025)) }, "pkgdesc"},
		{"long url", func(l []kv) []kv { return with(l, "url", strings.Repeat("u", 1025)) }, "url"},
		{"long license", func(l []kv) []kv { return with(l, "license", strings.Repeat("u", 1025)) }, "license"},
		{"long token", func(l []kv) []kv { return plus(l, "depend", strings.Repeat("t", 257)) }, "depend"},
		{"long provides token", func(l []kv) []kv { return plus(l, "provides", strings.Repeat("t", 257)) }, "provides"},
		{"long install_if token", func(l []kv) []kv { return with(l, "install_if", strings.Repeat("t", 257)) }, "install_if"},
		{"long name", func(l []kv) []kv { return with(l, "pkgname", strings.Repeat("n", 129)) }, "pkgname"},
		{"tab in description", func(l []kv) []kv { return with(l, "pkgdesc", "a\tb") }, "pkgdesc"},
		{"DEL in url", func(l []kv) []kv { return with(l, "url", "http://x\x7f") }, "url"},
		{"escape in license", func(l []kv) []kv { return with(l, "license", "\x1b[31mMIT") }, "license"},
		{"C1 control in maintainer", func(l []kv) []kv { return with(l, "maintainer", "a\u0085b") }, "maintainer"},
		{"NUL in origin", func(l []kv) []kv { return with(l, "origin", "a\x00b") }, "origin"},
		{"control in commit", func(l []kv) []kv { return with(l, "commit", "a\x01") }, "commit"},
		{"control in depend", func(l []kv) []kv { return plus(l, "depend", "a\x01b") }, "depend"},
		{"control in provides", func(l []kv) []kv { return plus(l, "provides", "a\x02b") }, "provides"},
		{"invalid UTF-8 in description", func(l []kv) []kv { return with(l, "pkgdesc", "a\xffb") }, "pkgdesc"},
		{"invalid UTF-8 in depend", func(l []kv) []kv { return plus(l, "depend", "a\xc3") }, "depend"},
		{"name with a slash", func(l []kv) []kv { return with(l, "pkgname", "a/b") }, "pkgname"},
		{"name with a space", func(l []kv) []kv { return with(l, "pkgname", "a b") }, "pkgname"},
		{"name starting with a dot", func(l []kv) []kv { return with(l, "pkgname", ".a") }, "pkgname"},
		{"name starting with a dash", func(l []kv) []kv { return with(l, "pkgname", "-a") }, "pkgname"},
		{"empty name", func(l []kv) []kv { return with(l, "pkgname", "") }, "pkgname"},
		{"version starting with v", func(l []kv) []kv { return with(l, "pkgver", "v1.2") }, "pkgver"},
		{"empty version", func(l []kv) []kv { return with(l, "pkgver", "") }, "pkgver"},
		{"version with a slash", func(l []kv) []kv { return with(l, "pkgver", "1/2") }, "pkgver"},
		{"version with a dash and a digit", func(l []kv) []kv { return with(l, "pkgver", "1.0-2") }, "pkgver"},
		{"arch with a dash", func(l []kv) []kv { return with(l, "arch", "x86-64") }, "arch"},
		{"arch with a slash", func(l []kv) []kv { return with(l, "arch", "../x") }, "arch"},
		{"empty arch", func(l []kv) []kv { return with(l, "arch", "") }, "arch"},
		{"datahash too short", func(l []kv) []kv { return with(l, "datahash", "AB") }, "datahash"},
		{"negative size", func(l []kv) []kv { return with(l, "size", "-1") }, "size"},
		{"signed size", func(l []kv) []kv { return with(l, "size", "+1") }, "size"},
		{"hex size", func(l []kv) []kv { return with(l, "size", "0x10") }, "size"},
		{"decimal size", func(l []kv) []kv { return with(l, "size", "1.5") }, "size"},
		{"empty size", func(l []kv) []kv { return with(l, "size", "") }, "size"},
		{"overflowing size", func(l []kv) []kv { return with(l, "size", "99999999999999999999") }, "size"},
		{"size beyond int64", func(l []kv) []kv { return with(l, "size", "9223372036854775808") }, "size"},
		{"builddate not a number", func(l []kv) []kv { return with(l, "builddate", "yesterday") }, "builddate"},
		{"negative provider priority", func(l []kv) []kv { return with(l, "provider_priority", "-1") }, "provider_priority"},
		{"provider priority over int32", func(l []kv) []kv { return with(l, "provider_priority", "2147483648") }, "provider_priority"},
		{"provider priority not a number", func(l []kv) []kv { return with(l, "provider_priority", "high") }, "provider_priority"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseInfo(info(tt.edit))
			if !errors.Is(err, ErrInvalidPackage) {
				t.Fatalf("got %v, want ErrInvalidPackage", err)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestParsePKGINFO_malformedLines(t *testing.T) {
	base := info(func(l []kv) []kv { return l })

	for _, line := range []string{"pkgdesc", "just some words", "{\"json\": true}", "C:Q1abc", "P:name"} {
		t.Run(line, func(t *testing.T) {
			if _, err := parseInfo(base + line + "\n"); !errors.Is(err, ErrInvalidPackage) {
				t.Fatalf("line %q accepted: %v", line, err)
			}
		})
	}
}

func TestParsePKGINFO_spacing(t *testing.T) {
	text := "pkgname=a\npkgver=1\n\tarch =\tnoarch  \ndatahash=" + strings.Repeat("a", 64) + "\ndepend=x\n"

	p, err := parseInfo(text)
	if err != nil {
		t.Fatal(err)
	}

	if p.Name != "a" || p.Version != "1" || p.Arch != "noarch" || !reflect.DeepEqual(p.Depends, []string{"x"}) {
		t.Fatalf("%+v", p)
	}
}

func TestParsePKGINFO_comments(t *testing.T) {
	text := "# pkgname = evil\n\n   # indented comment\n" + info(func(l []kv) []kv { return l })

	p, err := parseInfo(text)
	if err != nil {
		t.Fatal(err)
	}

	if p.Name != "hello" {
		t.Fatalf("name %q", p.Name)
	}
}

// TestParsePKGINFO_noInjection checks that a value cannot add a line or a
// field to the index: whatever the PKGINFO says, the rendered index holds one
// stanza with the same fields.
func TestParsePKGINFO_noInjection(t *testing.T) {
	for _, val := range []string{"x\nC:Q1AAAAAAAAAAAAAAAAAAAAAAAAAAA=\nP:evil", "x\r\nP:evil", "x\n\nP:evil", "x\u2028P:evil"} {
		text := info(func(l []kv) []kv { return with(l, "pkgdesc", val) })

		p, err := parseInfo(text)
		if err != nil {
			continue
		}

		pkgs, err := ParseIndex(strings.NewReader(string(RenderIndex([]Package{*p}))))
		if err != nil {
			t.Fatalf("value %q: rendered index does not parse: %v", val, err)
		}

		if len(pkgs) != 1 || pkgs[0].Name != "hello" {
			t.Fatalf("value %q injected %+v", val, pkgs)
		}
	}
}
