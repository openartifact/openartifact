// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gemversion implements RubyGems version ordering.
package gemversion

import (
	"cmp"
	"strings"
)

// gemSegment is one run of a version: a number (ASCII digits, kept as text
// without leading zeros so it has no size limit) or a string.
type gemSegment struct {
	text string
	num  bool
}

// gemVersionSegments splits a version into numeric and string segments.
func gemVersionSegments(v string) []gemSegment {
	var (
		segs []gemSegment
		cur  strings.Builder
		num  bool
	)

	flush := func() {
		if cur.Len() == 0 {
			return
		}

		if num {
			segs = append(segs, gemSegment{text: strings.TrimLeft(cur.String(), "0"), num: true})
		} else {
			segs = append(segs, gemSegment{text: cur.String()})
		}

		cur.Reset()
	}

	for _, r := range v {
		switch {
		case r == '.' || r == '-':
			flush()
		case r >= '0' && r <= '9':
			if !num {
				flush()
			}

			num = true

			cur.WriteRune(r)
		default:
			if num {
				flush()
			}

			num = false

			cur.WriteRune(r)
		}
	}

	flush()

	return segs
}

// Compare implements Gem::Version#<=>: numeric segments compare
// numerically, string segments (prerelease) sort before numbers.
func Compare(v1, v2 string) int {
	a, b := gemVersionSegments(v1), gemVersionSegments(v2)

	for i := 0; i < len(a) || i < len(b); i++ {
		// A missing segment is the number 0.
		x, y := gemSegment{num: true}, gemSegment{num: true}
		if i < len(a) {
			x = a[i]
		}

		if i < len(b) {
			y = b[i]
		}

		if c := compareSegments(x, y); c != 0 {
			return c
		}
	}

	return 0
}

func compareSegments(x, y gemSegment) int {
	switch {
	case !x.num && !y.num:
		return strings.Compare(x.text, y.text)
	case !x.num:
		return -1
	case !y.num:
		return 1
	default:
		// Numbers without leading zeros: the longer one is larger.
		return cmp.Or(cmp.Compare(len(x.text), len(y.text)), strings.Compare(x.text, y.text))
	}
}
