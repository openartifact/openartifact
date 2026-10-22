// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nuget holds NuGet protocol helpers shared by the hosted format
// handler and the proxy/group services: version parsing, ordering and
// normalization, and v3 registration/flat-container document handling.
package nuget

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

var errInvalidVersion = errors.New("invalid nuget version")

// Version is a parsed NuGet version: up to four numeric parts, optional
// release labels (the "-beta.1" part) and, which is not kept, build metadata.
type Version struct {
	parts   [4]uint32
	release []string // lower-cased dot-separated labels, empty for a stable version
}

// ParseVersion parses a NuGet version string such as "1.0", "1.0.0.0",
// "01.2.3-Beta.1+build5".
func ParseVersion(s string) (Version, error) {
	var v Version

	s = strings.TrimSpace(s)

	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !validLabels(s[i+1:]) {
			return v, errInvalidVersion
		}

		s = s[:i]
	}

	if i := strings.IndexByte(s, '-'); i >= 0 {
		if !validLabels(s[i+1:]) {
			return v, errInvalidVersion
		}

		v.release = strings.Split(strings.ToLower(s[i+1:]), ".")
		s = s[:i]
	}

	nums := strings.Split(s, ".")
	if len(nums) > len(v.parts) {
		return v, errInvalidVersion
	}

	for i, n := range nums {
		if n == "" || strings.Trim(n, "0123456789") != "" {
			return v, errInvalidVersion
		}

		p, err := strconv.ParseUint(n, 10, 32)
		if err != nil {
			return v, errInvalidVersion
		}

		v.parts[i] = uint32(p)
	}

	return v, nil
}

// validLabels reports whether s is a non-empty dot-separated list of non-empty
// labels made of [0-9A-Za-z-].
func validLabels(s string) bool {
	if s == "" {
		return false
	}

	for label := range strings.SplitSeq(s, ".") {
		if label == "" {
			return false
		}

		for _, c := range label {
			if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
				return false
			}
		}
	}

	return true
}

// String returns the normalized, lower-case form used in PackageBaseAddress
// URLs and stored in the database: three numeric parts (a fourth only when not
// zero), release labels lower-cased, build metadata dropped.
func (v Version) String() string {
	var b strings.Builder

	b.WriteString(strconv.FormatUint(uint64(v.parts[0]), 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(uint64(v.parts[1]), 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(uint64(v.parts[2]), 10))

	if v.parts[3] != 0 {
		b.WriteByte('.')
		b.WriteString(strconv.FormatUint(uint64(v.parts[3]), 10))
	}

	if len(v.release) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.release, "."))
	}

	return b.String()
}

// IsPrerelease reports whether v carries release labels.
func (v Version) IsPrerelease() bool { return len(v.release) > 0 }

// Compare orders versions per SemVer 2.0 extended with NuGet's fourth part:
// numeric parts first, then a version without release labels sorts after one
// with them, then labels pairwise (numeric labels numerically and before
// alphanumeric ones, alphanumeric labels in ordinal order).
func (v Version) Compare(o Version) int {
	if c := slices.Compare(v.parts[:], o.parts[:]); c != 0 {
		return c
	}

	switch {
	case len(v.release) == 0 && len(o.release) == 0:
		return 0
	case len(v.release) == 0:
		return 1
	case len(o.release) == 0:
		return -1
	}

	for i := 0; i < len(v.release) && i < len(o.release); i++ {
		if c := compareLabel(v.release[i], o.release[i]); c != 0 {
			return c
		}
	}

	return len(v.release) - len(o.release)
}

func compareLabel(a, b string) int {
	aNum := strings.Trim(a, "0123456789") == ""
	bNum := strings.Trim(b, "0123456789") == ""

	switch {
	case aNum && bNum:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			return len(a) - len(b)
		}

		return strings.Compare(a, b)
	case aNum:
		return -1
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// Normalize returns the normalized lower-case form of a NuGet version
// string. An unparsable string is returned lower-cased and trimmed with ok
// false: it cannot match any stored (normalized) version.
func Normalize(s string) (string, bool) {
	v, err := ParseVersion(s)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(s)), false
	}

	return v.String(), true
}

// CompareStrings orders two version strings; unparsable ones sort
// first, in string order.
func CompareStrings(a, b string) int {
	va, errA := ParseVersion(a)
	vb, errB := ParseVersion(b)

	switch {
	case errA != nil && errB != nil:
		return strings.Compare(a, b)
	case errA != nil:
		return -1
	case errB != nil:
		return 1
	}

	return va.Compare(vb)
}
