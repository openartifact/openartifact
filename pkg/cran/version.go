// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cran implements the parts of the R package repository protocol that
// do not depend on the server: the repository path grammar, the DESCRIPTION
// (Debian control file, "DCF") parser, R's version ordering, the PACKAGES index
// in its three encodings (plain, gzip, serialized R object) and the reading of
// DESCRIPTION out of a source or binary package archive.
//
// Every input is untrusted: parsers are bounded in size, never recurse and are
// fuzzed.
package cran

import (
	"strings"
)

// MaxVersionLen bounds a package version.
const MaxVersionLen = 64

// ValidVersion reports whether v is an R package version: non-negative
// integers separated by "." or "-" (R accepts both as one separator), such as
// "1.0", "0.9-3" or "2.1.0.9000". R itself requires at least two components for
// a package, but old repositories hold single-component versions and R's
// package_version() reads them, so one component is accepted.
func ValidVersion(v string) bool {
	if v == "" || len(v) > MaxVersionLen {
		return false
	}

	digits := 0

	for i := range len(v) {
		switch c := v[i]; {
		case c >= '0' && c <= '9':
			digits++
		case (c == '.' || c == '-') && digits > 0:
			digits = 0
		default:
			return false
		}
	}

	return digits > 0
}

// CompareVersions orders two valid versions the way R's package_version does:
// component by component as integers (so "1.10" > "1.9" and "1.0" == "1-0"
// in the components), a version that is a prefix of the other being the older.
// It returns -1, 0 or 1. Two spellings of one version that differ only in the
// separators or leading zeros compare equal, so callers that need a total order
// break the tie on the literal string.
func CompareVersions(a, b string) int {
	for a != "" && b != "" {
		var ca, cb string

		ca, a = nextComponent(a)
		cb, b = nextComponent(b)

		if c := compareDigits(ca, cb); c != 0 {
			return c
		}
	}

	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	default:
		return 1
	}
}

// Less is the total order used to pick the newest of several versions: R's
// ordering with the literal text as the tie-break.
func Less(a, b string) bool {
	if c := CompareVersions(a, b); c != 0 {
		return c < 0
	}

	return a < b
}

func nextComponent(s string) (string, string) {
	i := strings.IndexAny(s, ".-")
	if i < 0 {
		return s, ""
	}

	return s[:i], s[i+1:]
}

// compareDigits compares two non-empty decimal strings numerically without
// converting them, so a hostile component cannot overflow.
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")

	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}

	return strings.Compare(a, b)
}
