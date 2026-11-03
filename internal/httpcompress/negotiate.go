// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package httpcompress

import (
	"strconv"
	"strings"
)

// coding is a content coding the package can produce.
type coding uint8

const (
	codingNone coding = iota
	codingGzip
	codingZstd
)

// token is the Content-Encoding value of the coding.
func (c coding) token() string {
	switch c {
	case codingGzip:
		return "gzip"
	case codingZstd:
		return "zstd"
	case codingNone:
	}

	return ""
}

// negotiate picks the coding for an Accept-Encoding header value (RFC 9110
// section 12.5.3): the acceptable coding with the highest quality value, zstd
// on a tie, none when the client accepts neither or sends no header. An
// explicit q=0 refuses a coding; "*" stands for every coding not named, but is
// never used to select zstd while gzip is acceptable, so a client that only
// says "*" gets the coding every HTTP client can decode. The scan does not
// allocate.
func negotiate(header string) coding {
	if header == "" {
		return codingNone
	}

	const absent = -1.0

	qZstd, qGzip, qStar := absent, absent, absent

	for header != "" {
		var part string

		part, header, _ = strings.Cut(header, ",")
		name, params, _ := strings.Cut(part, ";")
		name = strings.TrimSpace(name)
		q := quality(params)

		switch {
		case strings.EqualFold(name, "zstd"):
			qZstd = q
		case strings.EqualFold(name, "gzip"), strings.EqualFold(name, "x-gzip"):
			qGzip = max(qGzip, q)
		case name == "*":
			qStar = q
		}
	}

	if qGzip == absent {
		qGzip = qStar
	}

	if qZstd == absent && qGzip <= 0 {
		qZstd = qStar
	}

	switch {
	case qZstd > 0 && qZstd >= qGzip:
		return codingZstd
	case qGzip > 0:
		return codingGzip
	}

	return codingNone
}

// quality reads the q parameter of one Accept-Encoding member; 1 when absent
// and 0 (refused) when malformed, so a client with a broken header never gets
// an encoding it may not have asked for.
func quality(params string) float64 {
	for params != "" {
		var p string

		p, params, _ = strings.Cut(params, ";")
		p = strings.TrimSpace(p)

		if len(p) < 2 || (p[0] != 'q' && p[0] != 'Q') {
			continue
		}

		v, ok := strings.CutPrefix(strings.TrimSpace(p[1:]), "=")
		if !ok {
			continue
		}

		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f < 0 || f > 1 {
			return 0
		}

		return f
	}

	return 1
}
