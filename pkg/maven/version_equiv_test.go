// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// refItem and the helpers below are the original string-switching
// implementation, kept as the oracle the optimized parser must agree with.
type refItem struct {
	num   int
	qual  string
	isNum bool
}

func (it refItem) isNull() bool {
	if it.isNum {
		return it.num == 0
	}

	return it.qual == ""
}

func refParse(v string) []refItem {
	v = strings.ToLower(v)

	var (
		items []refItem
		cur   strings.Builder
		curIs int
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

			items = append(items, refItem{isNum: true, num: n})

			return
		}

		if alias, ok := qualifierAlias(s); ok {
			s = alias
		}

		items = append(items, refItem{qual: s})
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

func refCompareItems(a, b refItem) int {
	switch {
	case a.isNum && b.isNum:
		return cmpInt(a.num, b.num)
	case a.isNum:
		if b.qual == "" {
			return cmpInt(a.num, 0)
		}

		return 1
	case b.isNum:
		return -refCompareItems(b, a)
	}

	ra, sa := rankQualifier(a.qual)
	rb, sb := rankQualifier(b.qual)

	if ra != rb {
		return cmpInt(ra, rb)
	}

	return strings.Compare(sa, sb)
}

func refCompare(a, b string) int {
	ia, ib := refParse(a), refParse(b)

	for i := 0; i < len(ia) || i < len(ib); i++ {
		var x, y refItem
		if i < len(ia) {
			x = ia[i]
		}

		if i < len(ib) {
			y = ib[i]
		}

		if c := refCompareItems(x, y); c != 0 {
			return c
		}
	}

	return 0
}

func equivVersions() []string {
	return []string{
		"", "0", "0.0.0", "1", "1.0", "1.0.0", "1.0-SNAPSHOT", "1.0-snapshot", "1.0.0-SNAPSHOT",
		"1.0-alpha", "1.0-a1", "1.0-alpha-2", "1.0-beta", "1.0-b2", "1.0-m1", "1.0-milestone-3",
		"1.0-rc1", "1.0-cr1", "1.0-RC2", "1.0-ga", "1.0-final", "1.0-release", "1.0.Final",
		"1.0-sp", "1.0-sp1", "1.0-foo", "1.0-bar", "1.0-FOO", "1.0.1", "1.0.10", "1.0.2", "1.10",
		"1.2.3.4.5.6.7.8.9.10", "2.0", "10.0", "1.0_1", "1_0", "1-0", "1.0--1", "1..0", ".1", "1.",
		"1a", "1a1", "a1", "alpha1", "1.0beta2", "1.0-beta2", "1.0.0.0.0.0.0.0.0.0.0.0.RELEASE",
		"4294967296", "99999999999999999999", "1.99999999999999999999", "٣.١", "1.٣", "é-1", "1.0-é",
		"1.0-\xff", "1.0-\xfe\xff", "latest", "LATEST", "1.0-20260102.030405-1", "1.0-SNAPSHOT-1",
	}
}

func TestVersionParsingMatchesReference(t *testing.T) {
	t.Parallel()

	for _, a := range equivVersions() {
		for _, b := range equivVersions() {
			if got, want := CompareVersions(a, b), refCompare(a, b); got != want {
				t.Fatalf("CompareVersions(%q, %q) = %d, reference %d", a, b, got, want)
			}
		}
	}
}

func TestSortVersionsMatchesPairwiseStableSort(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 2, 7, len(equivVersions())} {
		in := slices.Clone(equivVersions()[:n])
		// Reverse so ties (1.0 vs 1.0.0 ...) are exercised out of input order.
		slices.Reverse(in)

		want := slices.Clone(in)
		slices.SortStableFunc(want, refCompare)

		got := slices.Clone(in)
		SortVersions(got)

		if !slices.Equal(got, want) {
			t.Fatalf("n=%d: SortVersions = %q, want %q", n, got, want)
		}
	}
}

func FuzzVersionParsingMatchesReference(f *testing.F) {
	for _, v := range equivVersions() {
		f.Add(v, "1.0")
	}

	f.Fuzz(func(t *testing.T, a, b string) {
		if got, want := CompareVersions(a, b), refCompare(a, b); got != want {
			t.Fatalf("CompareVersions(%q, %q) = %d, reference %d", a, b, got, want)
		}
	})
}
