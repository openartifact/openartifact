// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/pathseg"
)

func TestCleanPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"a/b/c.jar", "a/b/c.jar", true},
		{"/a/b/c.jar", "a/b/c.jar", true},
		{"a/..b/c", "a/..b/c", true},
		{"", "", false},
		{"/", "", false},
		{"a/", "", false},
		{"a//b", "", false},
		{"//a", "", false},
		{"a/./b", "", false},
		{"a/../b", "", false},
		{"../a", "", false},
		{"a/..", "", false},
		{"a\\b", "", false},
		{"a/b\\..\\c", "", false},
		{"a/b\x00", "", false},
		{"a/b\n", "", false},
		{"a/b\x7f", "", false},
		{strings.Repeat("a", maxPathLen+1), "", false},
	}

	for _, tc := range tests {
		got, ok := CleanPath(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSafeNuGetSegment(t *testing.T) {
	for in, want := range map[string]bool{
		"newtonsoft.json": true, "1.0.0-rc.1+build": true, "a_b": true,
		"": false, ".": false, "..": false, "a/b": false, "a\\b": false, "a b": false, "a\x00": false,
		strings.Repeat("a", 257): false,
	} {
		if got := safeNuGetSegment(in); got != want {
			t.Errorf("safeNuGetSegment(%q) = %v, want %v", in, got, want)
		}
	}
}

// FuzzCleanPath holds CleanPath to its contract: an accepted path is the input
// minus one leading slash, made of plain segments only, with no control
// characters, so appending it to an upstream URL cannot leave the upstream's
// base path.
func FuzzCleanPath(f *testing.F) {
	for _, s := range []string{"", "/", "a", "/a/b", "a/", "a//b", "a/../b", "../a", "a\\b", "a\x00", "%2e%2e/a", "a/b/.."} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, p string) {
		got, ok := CleanPath(p)
		if !ok {
			if got != "" {
				t.Fatalf("CleanPath(%q) refused but returned %q", p, got)
			}

			return
		}

		if got != strings.TrimPrefix(p, "/") {
			t.Fatalf("CleanPath(%q) = %q, not the input without its leading slash", p, got)
		}

		if !pathseg.Path(got) || len(got) > maxPathLen {
			t.Fatalf("CleanPath(%q) accepted %q, which is not a bounded plain path", p, got)
		}

		for _, r := range got {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("CleanPath(%q) accepted a control character", p)
			}
		}
	})
}
