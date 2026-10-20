// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"strconv"
	"strings"
	"unicode"
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

type versionItem struct {
	num   int
	qual  string
	isNum bool
}

func (it versionItem) isNull() bool {
	if it.isNum {
		return it.num == 0
	}

	return it.qual == ""
}

func parseVersionItems(v string) []versionItem {
	v = strings.ToLower(v)

	var (
		items []versionItem
		cur   strings.Builder
		curIs int // 0 none, 1 digit, 2 letter
	)

	flush := func() {
		if cur.Len() == 0 {
			return
		}

		s := cur.String()
		cur.Reset()

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

		items = append(items, versionItem{qual: s})
	}

	for _, r := range v {
		switch {
		case r == '.' || r == '-' || r == '_':
			flush()

			curIs = 0
		case unicode.IsDigit(r):
			if curIs == 2 {
				flush()
			}

			curIs = 1

			cur.WriteRune(r)
		default:
			if curIs == 1 {
				flush()
			}

			curIs = 2

			cur.WriteRune(r)
		}
	}

	flush()

	for len(items) > 0 && items[len(items)-1].isNull() {
		items = items[:len(items)-1]
	}

	return items
}

func rankQualifier(q string) (int, string) {
	if r, ok := qualifierRank(q); ok {
		return r, ""
	}

	return 7, q
}

func compareItems(a, b versionItem) int {
	switch {
	case a.isNum && b.isNum:
		return cmpInt(a.num, b.num)
	case a.isNum:
		if b.qual == "" {
			return cmpInt(a.num, 0)
		}

		return 1
	case b.isNum:
		return -compareItems(b, a)
	}

	ra, sa := rankQualifier(a.qual)
	rb, sb := rankQualifier(b.qual)

	if ra != rb {
		return cmpInt(ra, rb)
	}

	return strings.Compare(sa, sb)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

// CompareVersions compares two Maven versions following the semantics of
// Maven's ComparableVersion (numeric segments numerically, well-known
// qualifiers alpha < beta < milestone < rc < snapshot < release < sp).
// It returns -1, 0 or 1.
func CompareVersions(a, b string) int {
	ia, ib := parseVersionItems(a), parseVersionItems(b)

	for i := 0; i < len(ia) || i < len(ib); i++ {
		var x, y versionItem
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
