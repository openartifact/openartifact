// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pub holds the naming rules, the request grammar, the package
// documents and the archive reader of the Dart and Flutter pub format: the
// hosted pub repository spec v2 served by pub.dev, spoken by "dart pub" and
// "flutter pub".
package pub

import (
	"regexp"
	"strings"
)

const (
	// MaxNameLen is the longest package name (pub.dev's limit).
	MaxNameLen = 64
	// MaxVersionLen is the longest package version.
	MaxVersionLen = 128
	// ArchiveSuffix ends every package archive file name.
	ArchiveSuffix = ".tar.gz"
)

// versionRe is the grammar of pub_semver: three numeric parts (leading zeros
// are tolerated, as pub.dev tolerates them), an optional pre-release and an
// optional build part. The shape keeps every version safe in a URL path, a file
// name and a cache key.
var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// reserved are the Dart reserved words, which "dart pub" refuses as package names.
var reserved = map[string]struct{}{ //nolint:gochecknoglobals // immutable word list
	"assert": {}, "break": {}, "case": {}, "catch": {}, "class": {}, "const": {}, "continue": {}, "default": {},
	"do": {}, "else": {}, "enum": {}, "extends": {}, "false": {}, "final": {}, "finally": {}, "for": {}, "if": {},
	"in": {}, "is": {}, "new": {}, "null": {}, "rethrow": {}, "return": {}, "super": {}, "switch": {}, "this": {},
	"throw": {}, "true": {}, "try": {}, "var": {}, "void": {}, "while": {}, "with": {},
}

// ValidName reports whether s is a valid package name: 1 to MaxNameLen
// lower-case letters, digits and underscores, not starting with a digit. The
// shape keeps every name safe in a URL path and a file name, and a name never
// holds "-", so an archive file name splits unambiguously.
func ValidName(s string) bool {
	if s == "" || len(s) > MaxNameLen || s[0] >= '0' && s[0] <= '9' {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}

	return true
}

// IsReserved reports whether name is a Dart reserved word.
func IsReserved(name string) bool {
	_, ok := reserved[name]

	return ok
}

// ValidVersion reports whether v is a package version (pub_semver).
func ValidVersion(v string) bool {
	return len(v) <= MaxVersionLen && versionRe.MatchString(v)
}

// split cuts a valid version into its numbers, pre-release and build parts.
func split(v string) ([3]string, string, string) {
	var (
		nums       [3]string
		pre, build string
	)

	core := v
	if i := strings.IndexByte(core, '+'); i >= 0 {
		core, build = core[:i], core[i+1:]
	}

	if i := strings.IndexByte(core, '-'); i >= 0 {
		core, pre = core[:i], core[i+1:]
	}

	copy(nums[:], strings.SplitN(core, ".", 3))

	return nums, pre, build
}

// IsPrerelease reports whether the valid version v has a pre-release part.
func IsPrerelease(v string) bool {
	_, pre, _ := split(v)

	return pre != ""
}

// CompareVersions orders valid versions by precedence, the way pub_semver does:
// the numbers, then a pre-release below its release, then the build part (a
// version with a build part is above the one without). The result is 0 only for
// identical numbers, pre-release and build.
func CompareVersions(a, b string) int {
	an, apre, abuild := split(a)
	bn, bpre, bbuild := split(b)

	for i := range an {
		if c := compareNumeric(an[i], bn[i]); c != 0 {
			return c
		}
	}

	switch {
	case apre == "" && bpre != "":
		return 1
	case apre != "" && bpre == "":
		return -1
	}

	if c := compareIdentifiers(apre, bpre); c != 0 {
		return c
	}

	switch {
	case abuild == "" && bbuild != "":
		return -1
	case abuild != "" && bbuild == "":
		return 1
	}

	return compareIdentifiers(abuild, bbuild)
}

// compareNumeric compares two digit strings of any length.
func compareNumeric(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}

		return 1
	}

	return strings.Compare(a, b)
}

func isDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return s != ""
}

// compareIdentifiers compares two dot-separated identifier lists: numeric ones
// numerically and below alphanumeric ones, a shorter prefix below a longer list.
func compareIdentifiers(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")

	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := as[i], bs[i]

		switch xd, yd := isDigits(x), isDigits(y); {
		case xd && yd:
			if c := compareNumeric(x, y); c != 0 {
				return c
			}
		case xd:
			return -1
		case yd:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}

	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}

	return 0
}

// ArchiveName is the archive file name of a package version.
func ArchiveName(name, version string) string {
	return name + "-" + version + ArchiveSuffix
}

// ParseArchiveName splits "name-version.tar.gz" and reports whether both parts
// are valid.
func ParseArchiveName(file string) (string, string, bool) {
	stem, found := strings.CutSuffix(file, ArchiveSuffix)
	if !found {
		return "", "", false
	}

	name, version, found := strings.Cut(stem, "-")
	if !found || !ValidName(name) || !ValidVersion(version) {
		return "", "", false
	}

	return name, version, true
}
