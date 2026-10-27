// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import "strings"

// Vercmp compares two package versions the way libalpm's alpm_pkg_vercmp does:
// "[epoch:]pkgver[-pkgrel]". It returns -1, 0 or 1. The release is compared
// only when both versions carry one.
func Vercmp(a, b string) int {
	if a == b {
		return 0
	}

	ea, va, ra := splitEVR(a)
	eb, vb, rb := splitEVR(b)

	if c := rpmvercmp(ea, eb); c != 0 {
		return c
	}

	if c := rpmvercmp(va, vb); c != 0 {
		return c
	}

	if ra != "" && rb != "" {
		return rpmvercmp(ra, rb)
	}

	return 0
}

// splitEVR splits "[epoch:]version[-release]". A missing epoch is "0"; the
// epoch is the run of digits before the first colon, as in libalpm.
func splitEVR(s string) (string, string, string) {
	epoch := "0"

	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}

	if i < len(s) && s[i] == ':' {
		if i > 0 {
			epoch = s[:i]
		}

		s = s[i+1:]
	}

	if j := strings.LastIndexByte(s, '-'); j >= 0 {
		return epoch, s[:j], s[j+1:]
	}

	return epoch, s, ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isAlnum(c byte) bool { return isDigit(c) || isAlpha(c) }

// rpmvercmp is the segment comparison of libalpm (a port of rpm's rpmvercmp):
// runs of digits compare as numbers, runs of letters as strings, anything else
// separates segments, and a numeric segment is newer than an alphabetic one.
//
//nolint:gocyclo,cyclop // a faithful port of libalpm, whose branches are its specification
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}

	one, two := 0, 0
	ptr1, ptr2 := 0, 0

	for one < len(a) && two < len(b) {
		for one < len(a) && !isAlnum(a[one]) {
			one++
		}

		for two < len(b) && !isAlnum(b[two]) {
			two++
		}

		// Ran to the end of either: finished with the loop.
		if one >= len(a) || two >= len(b) {
			break
		}

		// Different separator lengths: the longer separator is newer.
		if one-ptr1 != two-ptr2 {
			if one-ptr1 < two-ptr2 {
				return -1
			}

			return 1
		}

		ptr1, ptr2 = one, two

		isnum := isDigit(a[ptr1])
		if isnum {
			for ptr1 < len(a) && isDigit(a[ptr1]) {
				ptr1++
			}

			for ptr2 < len(b) && isDigit(b[ptr2]) {
				ptr2++
			}
		} else {
			for ptr1 < len(a) && isAlpha(a[ptr1]) {
				ptr1++
			}

			for ptr2 < len(b) && isAlpha(b[ptr2]) {
				ptr2++
			}
		}

		// The segments are of different types: numeric is newer than alpha.
		if two == ptr2 {
			if isnum {
				return 1
			}

			return -1
		}

		seg1, seg2 := a[one:ptr1], b[two:ptr2]

		if isnum {
			seg1 = strings.TrimLeft(seg1, "0")
			seg2 = strings.TrimLeft(seg2, "0")

			// The number with more digits wins.
			if len(seg1) != len(seg2) {
				if len(seg1) > len(seg2) {
					return 1
				}

				return -1
			}
		}

		if c := strings.Compare(seg1, seg2); c != 0 {
			return c
		}

		one, two = ptr1, ptr2
	}

	if one >= len(a) && two >= len(b) {
		return 0
	}

	// A remaining alphabetic tail never beats an empty string (1.0a < 1.0).
	if (one >= len(a) && !isAlpha(b[two])) || (one < len(a) && isAlpha(a[one])) {
		return -1
	}

	return 1
}
