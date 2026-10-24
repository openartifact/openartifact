// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"cmp"
	"testing"
)

// TestVersionOrderPreRelease: a label after zero components marks a
// pre-release of the shorter version, never a release newer than it, so a
// pre-release is not reported as the fix of its own release.
func TestVersionOrderPreRelease(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.0.0-rc", "1.0", -1},
		{"1.0.0-rc", "1.0.0", -1},
		{"1.0.0.0-rc1", "1.0", -1},
		{"1.0", "1.0.0-rc", 1},
		{"1.0.1", "1.0.0-rc", 1},
		{"1.0.0-rc", "1.0.0-rc", 0},
		{"1.0.0", "1.0", 0},
		{"1.0.0.1", "1.0", 1},
	} {
		ta, aok := versionTokens(tc.a)
		tb, bok := versionTokens(tc.b)

		if !aok || !bok {
			t.Fatalf("versionTokens(%q) or versionTokens(%q) failed", tc.a, tc.b)
		}

		if got := cmp.Compare(compareTokens(ta, tb), 0); got != tc.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}

	if fixed, ok := versionAtLeast("npm", "1.0.0-rc", "1.0"); !ok || fixed {
		t.Errorf("versionAtLeast(1.0.0-rc, 1.0) = %v, %v, want a known not-fixed", fixed, ok)
	}
}
