// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// qualifierRank orders well-known Maven qualifiers; "" is the release.
func qualifierRank(q string) (int, bool) {
	switch q {
	case "alpha":
		return 0, true
	case "beta":
		return 1, true
	case "milestone":
		return 2, true
	case "rc":
		return 3, true
	case "snapshot":
		return 4, true
	case "":
		return 5, true
	case "sp":
		return 6, true
	}

	return 0, false
}

func qualifierAlias(q string) (string, bool) {
	switch q {
	case "a":
		return "alpha", true
	case "b":
		return "beta", true
	case "m":
		return "milestone", true
	case "cr":
		return "rc", true
	case "ga", "final", "release":
		return "", true
	}

	return "", false
}

// versionItem is one parsed version segment. rank is the qualifier rank for
// non-numeric items (see rankQualifier) and is resolved at parse time so a
// comparison never switches on strings; qual only matters for unknown
// qualifiers (rank 7), which order lexically among themselves.
type versionItem struct {
	num   int
	qual  string
	rank  int
	isNum bool
}

func (it versionItem) isNull() bool {
	if it.isNum {
		return it.num == 0
	}

	return it.rank == releaseRank
}

// unknownQualifierRank orders qualifiers Maven does not know after "sp".
const unknownQualifierRank = 7

func isVersionDigit(r rune) bool {
	if r < utf8.RuneSelf {
		return '0' <= r && r <= '9'
	}

	return unicode.IsDigit(r)
}

// appendVersionItems parses v into items appended to dst. Segments are
// substrings of the lowered version, so parsing allocates only when dst has to
// grow (or when v contains upper-case letters).
func appendVersionItems(dst []versionItem, v string) []versionItem {
	v = strings.ToLower(v)
	base := len(dst)
	items := dst

	var (
		start int // first byte of the current segment
		curIs int // 0 none, 1 digit, 2 letter
	)

	flush := func(end int) {
		if end <= start {
			return
		}

		s := v[start:end]

		if curIs == 1 {
			n, err := strconv.Atoi(s)
			if err != nil {
				n = 1<<31 - 1
			}

			items = append(items, versionItem{isNum: true, num: n})

			return
		}

		if alias, ok := qualifierAlias(s); ok {
			s = alias
		}

		rank, qual := rankQualifier(s)
		items = append(items, versionItem{qual: qual, rank: rank})
	}

	for i, r := range v {
		switch {
		case r == '.' || r == '-' || r == '_':
			flush(i)

			curIs = 0
			start = i + 1
		case isVersionDigit(r):
			if curIs == 2 {
				flush(i)

				start = i
			}

			if curIs == 0 {
				start = i
			}

			curIs = 1
		default:
			if curIs == 1 {
				flush(i)

				start = i
			}

			if curIs == 0 {
				start = i
			}

			curIs = 2
		}
	}

	flush(len(v))

	for len(items) > base && items[len(items)-1].isNull() {
		items = items[:len(items)-1]
	}

	return items
}

// rankQualifier returns the qualifier's rank and, for unknown qualifiers only,
// the text they are ordered by.
func rankQualifier(q string) (int, string) {
	if r, ok := qualifierRank(q); ok {
		return r, ""
	}

	return unknownQualifierRank, q
}

func compareItems(a, b versionItem) int {
	switch {
	case a.isNum && b.isNum:
		return cmpInt(a.num, b.num)
	case a.isNum:
		if b.qual == "" && b.rank == releaseRank {
			return cmpInt(a.num, 0)
		}

		return 1
	case b.isNum:
		return -compareItems(b, a)
	}

	if a.rank != b.rank {
		return cmpInt(a.rank, b.rank)
	}

	return strings.Compare(a.qual, b.qual)
}

// releaseRank is the rank of the empty (release) qualifier.
const releaseRank = 5

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

// compareParsed compares two parsed item lists, padding the shorter with null
// items.
func compareParsed(ia, ib []versionItem) int {
	// A null item is the release qualifier (rank 5, empty text).
	null := versionItem{rank: releaseRank}

	for i := 0; i < len(ia) || i < len(ib); i++ {
		x, y := null, null
		if i < len(ia) {
			x = ia[i]
		}

		if i < len(ib) {
			y = ib[i]
		}

		if c := compareItems(x, y); c != 0 {
			return c
		}
	}

	return 0
}

// CompareVersions compares two Maven versions following the semantics of
// Maven's ComparableVersion (numeric segments numerically, well-known
// qualifiers alpha < beta < milestone < rc < snapshot < release < sp).
// It returns -1, 0 or 1.
//
// Comparing many versions against each other (sorting) should use
// SortVersions, which parses each version once instead of once per comparison.
func CompareVersions(a, b string) int {
	var bufA, bufB [8]versionItem

	return compareParsed(appendVersionItems(bufA[:0], a), appendVersionItems(bufB[:0], b))
}

type sortKey struct {
	version string
	items   []versionItem
}

// SortVersions sorts versions in place in Maven order (as CompareVersions),
// stably, parsing every version exactly once. All parsed segments share one
// backing array, so a sort of n versions costs O(1) allocations instead of
// O(n log n) per-comparison ones.
func SortVersions(versions []string) {
	if len(versions) < 2 {
		return
	}

	// Segments are appended to a single arena; the per-version slices are cut
	// afterwards so a growing arena never invalidates earlier keys.
	arena := make([]versionItem, 0, len(versions)*4)
	ends := make([]int, len(versions))

	for i, v := range versions {
		arena = appendVersionItems(arena, v)
		ends[i] = len(arena)
	}

	keys := make([]sortKey, len(versions))

	begin := 0
	for i, v := range versions {
		keys[i] = sortKey{version: v, items: arena[begin:ends[i]:ends[i]]}
		begin = ends[i]
	}

	slices.SortStableFunc(keys, func(a, b sortKey) int { return compareParsed(a.items, b.items) })

	for i := range keys {
		versions[i] = keys[i].version
	}
}
