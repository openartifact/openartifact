// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pyversion

import "testing"

func TestComparePEP440(t *testing.T) {
	order := []string{"1.0.dev1", "1.0a1", "1.0b2", "1.0rc1", "1.0", "1.0.post1", "1.1", "9.0.0", "10.0.0", "1!0.1"}
	for i := 1; i < len(order); i++ {
		if Compare(order[i-1], order[i]) >= 0 {
			t.Errorf("%s should sort below %s", order[i-1], order[i])
		}
	}

	if Compare("1.0", "1.0.0") != 0 {
		t.Error("1.0 and 1.0.0 should be equal")
	}
}

// TestCanonical: the spellings PEP 440 normalizes to one version agree, and
// versions that differ (local labels included) do not.
func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"1.0.0":            "1",
		"1.0":              "1",
		"v1.0":             "1",
		" 1.2.0 ":          "1.2",
		"01.002":           "1.2",
		"1.0.0rc1":         "1rc1",
		"1.0.0-rc1":        "1rc1",
		"1.0.0.RC.1":       "1rc1",
		"1.0c1":            "1rc1",
		"1.0preview2":      "1rc2",
		"2.0alpha1":        "2a1",
		"2.0-beta":         "2b0",
		"1.0-1":            "1.post1",
		"1.0.post1":        "1.post1",
		"1.0rev":           "1.post0",
		"1.0.dev":          "1.dev0",
		"1.0-dev3":         "1.dev3",
		"1.0a1.post2.dev3": "1a1.post2.dev3",
		"2!1.0":            "2!1",
		"0!1.0":            "1",
		"2.0.0+CPU":        "2+cpu",
		"2.0.0+cu121-1":    "2+cu121.1",
		"not a version":    "not a version",
	} {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}

	for _, pair := range [][2]string{{"1.0+cpu", "1.0+cu121"}, {"1.0rc1", "1.0"}, {"1.0.post1", "1.0"}, {"1.0", "1.0.1"}} {
		if Canonical(pair[0]) == Canonical(pair[1]) {
			t.Errorf("%q and %q canonicalize alike", pair[0], pair[1])
		}
	}
}

// FuzzCanonical: the canonical form of a version is a fixed point, and a valid
// version keeps its place in the order (Compare) under it.
func FuzzCanonical(f *testing.F) {
	for _, s := range []string{"1.0.0-RC1", "v2!1.0.post1.dev2+Local_1", "1.0-1", "01.0a", "x"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, v string) {
		c := Canonical(v)
		if again := Canonical(c); again != c {
			t.Fatalf("Canonical(%q) = %q, but Canonical(%q) = %q", v, c, c, again)
		}

		if Valid(v) && Compare(v, c) != 0 {
			t.Fatalf("Compare(%q, %q) = %d, want 0", v, c, Compare(v, c))
		}
	})
}

// TestHugeNumbersAreNotVersions: a number too large for an int does not make
// two versions equal, nor a dev release its final one; such a string is not a
// PEP 440 version here, and compares as text.
func TestHugeNumbersAreNotVersions(t *testing.T) {
	for _, pair := range [][2]string{
		{"1.0", "1.0.dev99999999999999999999"},
		{"1.0", "1.0.dev9223372036854775807"},
		{"99999999999999999998.0", "99999999999999999999.0"},
		{"1.0rc99999999999999999998", "1.0rc99999999999999999999"},
		{"1.0.post99999999999999999999", "1.0.post9223372036854775807"},
		{"99999999999999999999!1.0", "99999999999999999998!1.0"},
	} {
		if Canonical(pair[0]) == Canonical(pair[1]) || Compare(pair[0], pair[1]) == 0 {
			t.Errorf("%q and %q are one version: canonical %q and %q, Compare %d",
				pair[0], pair[1], Canonical(pair[0]), Canonical(pair[1]), Compare(pair[0], pair[1]))
		}
	}

	if Valid("1.0.dev9223372036854775807") || Valid("1.99999999999999999999") {
		t.Error("a version with a number that does not fit an int is valid")
	}

	// Leading zeros do not count, and 18 digits are fine (a timestamp version).
	if !Valid("000000000000000000000001.0") || !Valid("999999999999999999.20261116123456") ||
		Canonical("000000000000000000000001.0") != "1" {
		t.Error("a long number that fits was refused")
	}
}
