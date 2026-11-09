// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"strconv"
	"strings"
)

// gemValid reports whether v starts like a RubyGems version (a digit).
func gemValid(v string) bool {
	return v != "" && v[0] >= '0' && v[0] <= '9'
}

// splitEpoch cuts "[epoch:]rest"; a missing or non-numeric epoch is 0.
func splitEpoch(v string) (int, string) {
	if i := strings.IndexByte(v, ':'); i > 0 {
		if e, err := strconv.Atoi(v[:i]); err == nil {
			return e, v[i+1:]
		}
	}

	return 0, v
}

// dpkgCompare orders two Debian versions "[epoch:]upstream[-revision]" as
// dpkg --compare-versions does.
func dpkgCompare(a, b string) int {
	ea, ra := splitEpoch(a)
	eb, rb := splitEpoch(b)

	if ea != eb {
		return cmpInt(ea, eb)
	}

	ua, da := splitLast(ra)
	ub, db := splitLast(rb)

	if c := dpkgPart(ua, ub); c != 0 {
		return c
	}

	return dpkgPart(da, db)
}

func splitLast(s string) (string, string) {
	if before, after, ok := strings.CutLast(s, "-"); ok {
		return before, after
	}

	return s, ""
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

func isAlphaByte(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// dpkgOrder is the weight of a non-digit character: '~' sorts before the end
// of the string, letters before everything else.
func dpkgOrder(s string, i int) int {
	if i >= len(s) || isDigitByte(s[i]) {
		return 0
	}

	switch c := s[i]; {
	case c == '~':
		return -1
	case isAlphaByte(c):
		return int(c)
	default:
		return int(c) + 256
	}
}

func dpkgPart(a, b string) int {
	i, j := 0, 0

	for i < len(a) || j < len(b) {
		for (i < len(a) && !isDigitByte(a[i])) || (j < len(b) && !isDigitByte(b[j])) {
			oa, ob := dpkgOrder(a, i), dpkgOrder(b, j)
			if oa != ob {
				return cmpInt(oa, ob)
			}

			i++
			j++
		}

		var first int

		i, j, first = dpkgDigits(a, b, i, j)

		if i < len(a) && isDigitByte(a[i]) {
			return 1
		}

		if j < len(b) && isDigitByte(b[j]) {
			return -1
		}

		if first != 0 {
			return first
		}
	}

	return 0
}

// dpkgDigits compares the digit runs at i and j (leading zeros ignored) and
// returns the cursors past the common length plus the first differing digit.
func dpkgDigits(a, b string, i, j int) (int, int, int) {
	for i < len(a) && a[i] == '0' {
		i++
	}

	for j < len(b) && b[j] == '0' {
		j++
	}

	first := 0

	for i < len(a) && isDigitByte(a[i]) && j < len(b) && isDigitByte(b[j]) {
		if first == 0 {
			first = cmpInt(int(a[i]), int(b[j]))
		}

		i++
		j++
	}

	return i, j, first
}

// rpmCompare orders "[epoch:]version[-release]" as rpm does; the release is
// compared only when both versions carry one.
func rpmCompare(a, b string) int {
	ea, ra := splitEpoch(a)
	eb, rb := splitEpoch(b)

	if ea != eb {
		return cmpInt(ea, eb)
	}

	va, relA := splitLast(ra)
	vb, relB := splitLast(rb)

	if c := rpmvercmp(va, vb); c != 0 {
		return c
	}

	if relA != "" && relB != "" {
		return rpmvercmp(relA, relB)
	}

	return 0
}

func isAlnumByte(c byte) bool { return isDigitByte(c) || isAlphaByte(c) }

// rpmvercmp is rpm's segment comparison with "~" (sorts before anything,
// even the end) and "^" (after the end, before any further segment).
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}

	i, j := 0, 0

	for i < len(a) || j < len(b) {
		i, j = skipSeparators(a, i), skipSeparators(b, j)

		if c, done := rpmMarker(a, b, &i, &j); done {
			if c != 0 {
				return c
			}

			continue
		}

		if i >= len(a) || j >= len(b) {
			break
		}

		if c := rpmSegment(a, b, &i, &j); c != 0 {
			return c
		}
	}

	switch {
	case i >= len(a) && j >= len(b):
		return 0
	case i >= len(a):
		return -1
	default:
		return 1
	}
}

func skipSeparators(s string, i int) int {
	for i < len(s) && !isAlnumByte(s[i]) && s[i] != '~' && s[i] != '^' {
		i++
	}

	return i
}

// rpmMarker handles "~" and "^" at the cursors. done reports the cursors sat
// on a marker; c is then the verdict (0 to keep comparing).
func rpmMarker(a, b string, i, j *int) (int, bool) {
	ta, tb := *i < len(a) && a[*i] == '~', *j < len(b) && b[*j] == '~'
	if ta || tb {
		if !ta {
			return 1, true
		}

		if !tb {
			return -1, true
		}

		*i++
		*j++

		return 0, true
	}

	ca, cb := *i < len(a) && a[*i] == '^', *j < len(b) && b[*j] == '^'
	if !ca && !cb {
		return 0, false
	}

	switch {
	case *i >= len(a):
		return -1, true
	case *j >= len(b):
		return 1, true
	case !ca:
		return 1, true
	case !cb:
		return -1, true
	}

	*i++
	*j++

	return 0, true
}

// rpmSegment compares the next alphabetic or numeric run of each side.
func rpmSegment(a, b string, i, j *int) int {
	si, sj := *i, *j
	num := isDigitByte(a[si])
	kind := isAlphaByte

	if num {
		kind = isDigitByte
	}

	for *i < len(a) && kind(a[*i]) {
		*i++
	}

	for *j < len(b) && kind(b[*j]) {
		*j++
	}

	sa, sb := a[si:*i], b[sj:*j]
	if sb == "" {
		// segments of different kinds: numeric is newer
		if num {
			return 1
		}

		return -1
	}

	if num {
		sa, sb = strings.TrimLeft(sa, "0"), strings.TrimLeft(sb, "0")
		if len(sa) != len(sb) {
			return cmpInt(len(sa), len(sb))
		}
	}

	return strings.Compare(sa, sb)
}
