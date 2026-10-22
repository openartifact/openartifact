// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"slices"
	"testing"
)

func TestNormalizeVersion(t *testing.T) {
	valid := map[string]string{
		"1.0.0":                  "1.0.0",
		"1.0":                    "1.0.0",
		"1":                      "1.0.0",
		"1.0.0.0":                "1.0.0",
		"1.2.3.0":                "1.2.3",
		"1.2.3.4":                "1.2.3.4",
		"01.02.03":               "1.2.3",
		"001.0.00":               "1.0.0",
		"13.0.3":                 "13.0.3",
		"1.0.0-Beta":             "1.0.0-beta",
		"1.0-BETA.2":             "1.0.0-beta.2",
		"1.0.0+build.5":          "1.0.0",
		"1.0.0-rc.1+sha.abc123":  "1.0.0-rc.1",
		"1.0.0.0-RC1":            "1.0.0-rc1",
		"2.0.0-alpha-1":          "2.0.0-alpha-1",
		"1.0.0-0":                "1.0.0-0",
		"4.7.0.0+meta":           "4.7.0",
		"10.0.0-preview.1.24080": "10.0.0-preview.1.24080",
	}

	if got, ok := normalizeVersion(" \t1.0.0 "); !ok || got != "1.0.0" {
		t.Errorf("surrounding whitespace: normalizeVersion = %q, %v", got, ok)
	}

	for in, want := range valid {
		got, ok := normalizeVersion(in)
		if !ok || got != want {
			t.Errorf("normalizeVersion(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}

	for _, in := range []string{"", "a", "1.a", "1..0", "1.0.0.0.0", "1.0.0-", "1.0.0-be ta", "-1.0.0", "1.0.0-a..b", "1.0.0+", "99999999999.0.0"} {
		if got, ok := normalizeVersion(in); ok {
			t.Errorf("normalizeVersion(%q) = %q, want rejection", in, got)
		}
	}
}

// The normalized form is a fixed point and equal for every spelling of a version.
func TestNormalizeVersionIdempotent(t *testing.T) {
	for _, in := range []string{"1.0", "1.0.0.0", "01.0.0", "1.0.0+x", "1.0.0-Beta.01"} {
		a, _ := normalizeVersion(in)

		b, ok := normalizeVersion(a)
		if !ok || a != b {
			t.Errorf("normalizeVersion(%q) = %q, again = %q", in, a, b)
		}
	}
}

func TestCompareVersionStrings(t *testing.T) {
	ordered := []string{
		"0.9.0",
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
		"1.0.0.1",
		"1.0.1",
		"1.2.0",
		"1.10.0",
		"2.0.0",
		"13.0.3",
	}

	for i, a := range ordered {
		for j, b := range ordered {
			got := compareVersionStrings(a, b)

			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}

			if got != want {
				t.Errorf("compareVersionStrings(%q, %q) = %d, want %d", a, b, got, want)
			}
		}
	}

	// Case, leading zeros, a zero fourth part and build metadata do not matter.
	for _, pair := range [][2]string{{"1.0.0", "1.0"}, {"1.0.0", "1.0.0.0"}, {"1.0.0-RC1", "1.0.0-rc1"}, {"1.0.0+a", "1.0.0+b"}, {"01.0.0", "1.0.0"}} {
		if got := compareVersionStrings(pair[0], pair[1]); got != 0 {
			t.Errorf("compareVersionStrings(%q, %q) = %d, want 0", pair[0], pair[1], got)
		}
	}
}

func TestSortedVersionsOrder(t *testing.T) {
	in := []string{"2.0.0", "1.10.0", "1.2.0", "1.0.0-beta", "1.0.0"}
	want := []string{"1.0.0-beta", "1.0.0", "1.2.0", "1.10.0", "2.0.0"}

	sorted := slices.Clone(in)
	slices.SortFunc(sorted, compareVersionStrings)

	if !slices.Equal(sorted, want) {
		t.Errorf("sorted = %v, want %v", sorted, want)
	}
}
