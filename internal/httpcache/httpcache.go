// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpcache holds the dependency-free pieces of HTTP validator
// caching shared by the SPA handler and the format handlers: If-None-Match
// evaluation and Cache-Control construction.
package httpcache

import (
	"strconv"
	"strings"
)

// ETagMatches reports whether an If-None-Match header value matches etag using
// the weak comparison RFC 9110 prescribes for If-None-Match: the W/ prefix is
// ignored and "*" matches any current representation.
func ETagMatches(header, etag string) bool {
	if header == "" {
		return false
	}

	etag = strings.TrimPrefix(etag, "W/")

	for c := range strings.SplitSeq(header, ",") {
		c = strings.TrimPrefix(strings.TrimSpace(c), "W/")
		if c == "*" || c == etag {
			return true
		}
	}

	return false
}

// CacheControl builds the Cache-Control value of a response that is
// revalidated with a validator. A response that only an anonymous caller with
// read access to a public repository could have received may be stored by
// shared caches; everything else is private to the requesting client and must
// be revalidated on every use. maxAgeSeconds <= 0 means "always revalidate".
func CacheControl(shared bool, maxAgeSeconds int) string {
	switch {
	case !shared:
		return "private, no-cache"
	case maxAgeSeconds <= 0:
		return "public, no-cache"
	default:
		return "public, max-age=" + strconv.Itoa(maxAgeSeconds)
	}
}
