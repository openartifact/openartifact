// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pathseg is the one definition of a plain path segment, shared by
// every place that turns client-supplied names into repository paths,
// coordinates, file names or upstream URLs.
//
// A plain segment is not empty, not "." or "..", and holds no "/", backslash
// or NUL. A name that passes can neither climb out of the directory it is
// joined to nor alias a sibling ("a//b" against "a/b"), whichever separator a
// client, file system or archive tool reads into it.
//
// The functions work on decoded text. A caller that holds a still
// percent-encoded path (a URL's EscapedPath) uses HasParentRaw, which decodes
// each segment before looking at it; decoding a value that is already final
// would turn a literal "%" into an escape of its own.
//
// Callers add their own character-set and length rules on top; this package
// only answers the traversal question.
package pathseg

import (
	"net/url"
	"os"
	"strings"
)

// Name reports whether s is one plain segment: a file or directory name that
// is not empty, not "." or "..", and has no "/", backslash or NUL.
func Name(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

// OptionalName is Name for a route parameter that may be absent: the empty
// string passes, anything else must be a plain segment.
func OptionalName(s string) bool {
	return s == "" || Name(s)
}

// Segments reports whether every element of an already split path is a plain
// segment. An empty list passes; callers that need a minimum depth check it.
func Segments(parts []string) bool {
	for _, s := range parts {
		if !Name(s) {
			return false
		}
	}

	return true
}

// Path reports whether p is a relative, slash-separated path of plain
// segments: not empty, no leading or trailing slash, no empty segment, no
// "." or ".." segment, no backslash or NUL.
func Path(p string) bool {
	if p == "" {
		return false
	}

	for seg := range strings.SplitSeq(p, "/") {
		if !Name(seg) {
			return false
		}
	}

	return true
}

// HasParent reports whether p, an operator-supplied or archive-supplied path of
// any shape (absolute, with empty segments, with either separator), has a ".."
// segment. It is the check for paths that are legitimately not plain, such as
// an absolute storage directory, where Path would refuse the leading slash.
func HasParent(p string) bool {
	for seg := range strings.FieldsFuncSeq(p, isSeparator) {
		if seg == ".." {
			return true
		}
	}

	return false
}

// HasParentRaw is HasParent for a still percent-encoded path, as
// url.URL.EscapedPath returns it: every segment is decoded first, so "%2e%2e"
// and "..%2f" count, and an escape that does not decode counts as a hit.
func HasParentRaw(escaped string) bool {
	for seg := range strings.SplitSeq(escaped, "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil || HasParent(dec) {
			return true
		}
	}

	return false
}

// Escapes reports whether rel, a path made relative to a base by
// filepath.Rel or path.Clean, leads out of that base: it is ".." or starts
// with "../" (or the OS separator form).
func Escapes(rel string) bool {
	if rel == ".." {
		return true
	}

	return strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func isSeparator(r rune) bool { return r == '/' || r == '\\' }
