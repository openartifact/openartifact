// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/openartifact/openartifact/pkg/maven"
)

// versionAtLeast reports whether version >= fixed. ok is false when the two
// cannot be compared reliably (non-numeric leading component), in which case
// callers must not claim the vulnerability is fixed. Maven-style formats use
// the Maven comparator; everything else compares dot/dash separated tokens
// numerically, with a trailing alphabetic tail ("rc1", "beta") sorting before
// the bare release.
func versionAtLeast(format, version, fixed string) (bool, bool) {
	version, fixed = strings.TrimSpace(version), strings.TrimSpace(fixed)
	if version == "" || fixed == "" || version == "unknown" {
		return false, false
	}

	switch strings.ToLower(format) {
	case "maven", "ivy", "sbt":
		return maven.CompareVersions(version, fixed) >= 0, true
	}

	a, aok := versionTokens(version)
	b, bok := versionTokens(fixed)

	if !aok || !bok {
		return false, false
	}

	return compareTokens(a, b) >= 0, true
}

// fixedByAny handles scanners that list several fixed versions ("1.2.9, 2.0.4"):
// the artifact is fixed when it is at least the fix of its own major line.
func fixedByAny(format, version, fixedIn string) bool {
	for f := range strings.SplitSeq(fixedIn, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}

		if strings.Contains(fixedIn, ",") && majorOf(f) != majorOf(version) {
			continue
		}

		if ge, ok := versionAtLeast(format, version, f); ok && ge {
			return true
		}
	}

	return false
}

func majorOf(v string) string {
	v = strings.TrimLeft(strings.TrimSpace(v), "vV")
	if i := strings.IndexFunc(v, func(r rune) bool { return !unicode.IsDigit(r) }); i >= 0 {
		return v[:i]
	}

	return v
}

type vtoken struct {
	num   int
	alpha string
	isNum bool
}

func versionTokens(v string) ([]vtoken, bool) {
	v = strings.TrimLeft(v, "vV")
	if v == "" || !unicode.IsDigit(rune(v[0])) {
		return nil, false
	}

	var toks []vtoken

	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		// split "rc1" into "rc", 1
		for f != "" {
			i := 0
			isDigit := unicode.IsDigit(rune(f[0]))

			for i < len(f) && unicode.IsDigit(rune(f[i])) == isDigit {
				i++
			}

			part := f[:i]
			f = f[i:]

			if isDigit {
				n, err := strconv.Atoi(part)
				if err != nil {
					return nil, false
				}

				toks = append(toks, vtoken{num: n, isNum: true})
			} else {
				toks = append(toks, vtoken{alpha: strings.ToLower(part)})
			}
		}
	}

	return toks, len(toks) > 0
}

func compareTokens(a, b []vtoken) int {
	for i := range max(len(a), len(b)) {
		switch {
		case i >= len(a):
			return -tailOrder(b[i:])
		case i >= len(b):
			return tailOrder(a[i:])
		}

		x, y := a[i], b[i]

		switch {
		case x.isNum && y.isNum:
			if x.num != y.num {
				return cmpInt(x.num, y.num)
			}
		case x.isNum != y.isNum:
			// a release number beats a pre-release label at the same position
			if x.isNum {
				return 1
			}

			return -1
		default:
			if c := strings.Compare(x.alpha, y.alpha); c != 0 {
				return c
			}
		}
	}

	return 0
}

// tailOrder is +1 when the extra tokens make the longer version newer
// (a further number, "1.0" < "1.0.1"), -1 when they mark a pre-release
// ("1.0.0-rc1" < "1.0.0"), 0 for trailing zeros. The first token that is not a
// zero decides: zeros before a label do not make the version newer ("1.0.0-rc"
// is older than "1.0").
func tailOrder(rest []vtoken) int {
	for _, t := range rest {
		switch {
		case !t.isNum:
			return -1
		case t.num != 0:
			return 1
		}
	}

	return 0
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}

	return 1
}
