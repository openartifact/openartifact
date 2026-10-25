// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pathseg

import (
	"net/url"
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"lib-1.0.jar", true},
		{"a b", true},
		{"..foo", true},
		{"foo..", true},
		{"...", true},
		{"a%2e%2e", true}, // decoded text: a literal percent sign is a plain character
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"/", false},
		{"a\\b", false},
		{"..\\", false},
		{"a\x00b", false},
		{"\x00", false},
	}

	for _, tc := range tests {
		if got := Name(tc.in); got != tc.want {
			t.Errorf("Name(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestOptionalName(t *testing.T) {
	for in, want := range map[string]bool{"": true, "rev1": true, ".": false, "..": false, "a/b": false, "a\\b": false} {
		if got := OptionalName(in); got != want {
			t.Errorf("OptionalName(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSegments(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  bool
	}{
		{"plain", []string{"org", "mod", "1.0", "mod-1.0.jar"}, true},
		{"none", nil, true},
		{"empty segment", []string{"a", "", "b"}, false},
		{"dot", []string{"a", ".", "b"}, false},
		{"dot-dot", []string{"..", "a"}, false},
		{"backslash", []string{"a", "b\\c"}, false},
		{"nul", []string{"a", "b\x00"}, false},
		{"slash inside a part", []string{"a/b"}, false},
	}

	for _, tc := range tests {
		if got := Segments(tc.parts); got != tc.want {
			t.Errorf("%s: Segments(%q) = %v, want %v", tc.name, tc.parts, got, tc.want)
		}
	}
}

func TestPath(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"a", true},
		{"a/b/c.txt", true},
		{"pool/main/f/foo/foo_1.0_amd64.deb", true},
		{"a/..b/c", true},
		{"", false},
		{"/a", false},
		{"a/", false},
		{"a//b", false},
		{"a/./b", false},
		{"a/../b", false},
		{"../a", false},
		{"a/..", false},
		{".", false},
		{"a\\b", false},
		{"a/b\\..\\c", false},
		{"a/b\x00", false},
	}

	for _, tc := range tests {
		if got := Path(tc.in); got != tc.want {
			t.Errorf("Path(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestHasParent(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"..", true},
		{"../a", true},
		{"a/..", true},
		{"a/../b", true},
		{"/abs/../b", true},
		{"a\\..\\b", true},
		{"..\\a", true},
		{"/var/lib/oa", false},
		{"/var//lib/./oa", false},
		{"", false},
		{"..a", false},
		{"a..", false},
		{"a..b/c", false},
		{"...", false},
	}

	for _, tc := range tests {
		if got := HasParent(tc.in); got != tc.want {
			t.Errorf("HasParent(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestHasParentRaw(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"/repos/r/lodash", false},
		{"/repos/npm/@scope%2Fname", false},
		{"/repos/r/a%20b", false},
		{"/repos/r/a..b", false},
		{"/repos/r/100%25", false},
		{"/repos/r/../x", true},
		{"/repos/r/%2e%2e/x", true},
		{"/repos/r/%2E%2E/x", true},
		{"/repos/r/.%2e/x", true},
		{"/repos/r/..%2fx", true},
		{"/repos/r/%2e%2e%5cx", true},
		{"/repos/r/a%5c..%5cb", true},
		{"/repos/r/..%5c", true},
		{"/repos/r/%zz", true}, // an escape that does not decode is refused
		{"/repos/r/%", true},
	}

	for _, tc := range tests {
		if got := HasParentRaw(tc.in); got != tc.want {
			t.Errorf("HasParentRaw(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestEscapes(t *testing.T) {
	for in, want := range map[string]bool{
		"..":      true,
		"../a":    true,
		"../../a": true,
		".":       false,
		"a":       false,
		"a/../b":  false, // a cleaned relative path never has this shape
		"..a":     false,
		"..a/b":   false,
		"a/..":    false,
	} {
		if got := Escapes(in); got != want {
			t.Errorf("Escapes(%q) = %v, want %v", in, got, want)
		}
	}
}

// refName is the contract of Name written the long way round: a segment is
// plain when it is non-empty, is not a dot segment and none of its bytes is a
// separator or NUL.
func refName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}

	for i := range len(s) {
		if s[i] == '/' || s[i] == '\\' || s[i] == 0 {
			return false
		}
	}

	return true
}

// FuzzPathseg holds every validator to the contract: Path is exactly "every
// slash-separated segment is plain", Segments and Name agree with it, a path
// they accept never has a parent reference under either separator, and the raw
// form agrees with the decoded one.
func FuzzPathseg(f *testing.F) {
	for _, s := range []string{
		"", ".", "..", "a", "a/b", "a//b", "/a", "a/", "a/../b", "a\\b", "a\x00b",
		"%2e%2e", "a%2f..%2fb", "..%5c", "%zz", "..\\", "a/b\\..\\c", "...", "a..b",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, p string) {
		if got, want := Name(p), refName(p); got != want {
			t.Fatalf("Name(%q) = %v, want %v", p, got, want)
		}

		parts := strings.Split(p, "/")

		want := p != ""
		for _, s := range parts {
			want = want && refName(s)
		}

		if got := Path(p); got != want {
			t.Fatalf("Path(%q) = %v, want %v", p, got, want)
		}

		if p != "" && Segments(parts) != Path(p) {
			t.Fatalf("Segments(%q) = %v disagrees with Path = %v", parts, Segments(parts), Path(p))
		}

		if Path(p) && (HasParent(p) || strings.ContainsAny(p, "\\\x00")) {
			t.Fatalf("Path(%q) accepted a path with a parent reference, backslash or NUL", p)
		}

		if Name(p) && HasParent(p) {
			t.Fatalf("Name(%q) accepted a parent reference", p)
		}

		// Raw callers see the escaped form of p: a path that is plain must stay
		// free of parent references whatever the escaping, and escaping a plain
		// path never makes the raw check fire.
		if Path(p) && HasParentRaw(escapeSegments(p)) {
			t.Fatalf("HasParentRaw reports a parent in the escaped plain path %q", p)
		}

		// A raw path is judged by what it decodes to.
		if dec, err := url.PathUnescape(p); err == nil && !strings.Contains(p, "/") && !HasParentRaw(p) && HasParent(dec) {
			t.Fatalf("HasParentRaw(%q) missed that it decodes to %q", p, dec)
		}
	})
}

func escapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}

	return strings.Join(parts, "/")
}
