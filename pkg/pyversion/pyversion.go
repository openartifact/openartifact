// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pyversion

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

// pep440Re matches the public version scheme of PEP 440 (after lowercasing).
var pep440Re = regexp.MustCompile(
	`^v?(?:(\d+)!)?(\d+(?:\.\d+)*)(?:[-_.]?(a|b|c|rc|alpha|beta|pre|preview)[-_.]?(\d*))?` +
		`(?:(?:-(\d+))|(?:[-_.]?(post|rev|r)[-_.]?(\d*)))?(?:[-_.]?(dev)[-_.]?(\d*))?(?:\+.*)?$`,
)

// pep440Key is a parsed version, comparable via Compare.
type pep440Key struct {
	epoch    int
	release  []int
	preRank  int // 0 dev-only, 1 a, 2 b, 3 rc, 4 final
	preNum   int
	post     int // -1 when absent
	dev      int // maxInt when absent
	original string
}

const noDev = int(^uint(0) >> 1)

func parse(v string) (pep440Key, bool) {
	m := pep440Re.FindStringSubmatch(strings.ToLower(strings.TrimSpace(v)))
	if m == nil {
		return pep440Key{}, false
	}

	k := pep440Key{original: v, preRank: 4, post: -1, dev: noDev}
	k.epoch, _ = strconv.Atoi(m[1])

	for part := range strings.SplitSeq(m[2], ".") {
		n, _ := strconv.Atoi(part)
		k.release = append(k.release, n)
	}

	for len(k.release) > 1 && k.release[len(k.release)-1] == 0 {
		k.release = k.release[:len(k.release)-1]
	}

	if m[3] != "" {
		switch m[3] {
		case "a", "alpha":
			k.preRank = 1
		case "b", "beta":
			k.preRank = 2
		default:
			k.preRank = 3
		}

		k.preNum, _ = strconv.Atoi(m[4])
	}

	switch {
	case m[5] != "":
		k.post, _ = strconv.Atoi(m[5])
	case m[6] != "":
		k.post, _ = strconv.Atoi(m[7])
	}

	if m[8] != "" {
		k.dev, _ = strconv.Atoi(m[9])
		// A dev-only release sorts before every pre-release of its number.
		if k.preRank == 4 && k.post < 0 {
			k.preRank = 0
		}
	}

	return k, true
}

// Compare orders two versions; versions that do not parse sort below
// every valid one and among themselves lexically.
func Compare(a, b string) int {
	ka, okA := parse(a)
	kb, okB := parse(b)

	switch {
	case !okA && !okB:
		return strings.Compare(a, b)
	case !okA:
		return -1
	case !okB:
		return 1
	}

	if c := cmp.Compare(ka.epoch, kb.epoch); c != 0 {
		return c
	}

	for i := 0; i < max(len(ka.release), len(kb.release)); i++ {
		var x, y int
		if i < len(ka.release) {
			x = ka.release[i]
		}

		if i < len(kb.release) {
			y = kb.release[i]
		}

		if c := cmp.Compare(x, y); c != 0 {
			return c
		}
	}

	return cmp.Or(
		cmp.Compare(ka.preRank, kb.preRank),
		cmp.Compare(ka.preNum, kb.preNum),
		cmp.Compare(ka.post, kb.post),
		cmp.Compare(ka.dev, kb.dev),
	)
}

// Valid reports whether v parses as a PEP 440 version.
func Valid(v string) bool {
	_, ok := parse(v)

	return ok
}
