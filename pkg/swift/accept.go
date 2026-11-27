// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"net/http"
	"strings"
)

// Media types of the registry API; the version and the "+json", "+swift" or
// "+zip" suffix are what a client puts in its Accept header.
const (
	// MediaJSON is the media type of the JSON documents.
	MediaJSON = "application/vnd.swift.registry.v1+json"
	// MediaZip is the media type of the source archive.
	MediaZip = "application/vnd.swift.registry.v1+zip"
	// MediaSwift is the media type of the manifest.
	MediaSwift = "application/vnd.swift.registry.v1+swift"

	// ContentTypeProblem is the media type of an error body (RFC 7807).
	ContentTypeProblem = "application/problem+json"
	// ContentTypeManifest is what a manifest is served as.
	ContentTypeManifest = "text/x-swift"
	// ContentTypeZip is what a source archive is served as.
	ContentTypeZip = "application/zip"

	mediaFamily = "application/vnd.swift.registry"
)

// CheckAccept judges the Accept header values of a request. A request without
// one, or with only media types outside the registry family ("*/*",
// "application/json"), is processed as version 1 (http.StatusOK). Otherwise at
// least one range must name version 1 (a range without a version means 1): when
// none does, the answer is http.StatusUnsupportedMediaType if a range named a
// well-formed other version and http.StatusBadRequest if one was malformed.
func CheckAccept(values []string) int {
	family, unsupported, malformed := false, false, false

	for _, v := range values {
		for rng := range strings.SplitSeq(v, ",") {
			rng, _, _ = strings.Cut(rng, ";")
			rng = strings.ToLower(strings.TrimSpace(rng))

			rest, ok := strings.CutPrefix(rng, mediaFamily)
			if !ok {
				continue
			}

			family = true

			rest, _, _ = strings.Cut(rest, "+")

			switch {
			case rest == "" || rest == ".v1":
				return http.StatusOK
			case wellFormedVersion(rest):
				unsupported = true
			default:
				malformed = true
			}
		}
	}

	switch {
	case !family:
		return http.StatusOK
	case unsupported:
		return http.StatusUnsupportedMediaType
	case malformed:
		return http.StatusBadRequest
	}

	return http.StatusBadRequest
}

// wellFormedVersion reports whether s is ".v" and digits.
func wellFormedVersion(s string) bool {
	digits, ok := strings.CutPrefix(s, ".v")
	if !ok || digits == "" {
		return false
	}

	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}

	return true
}
