// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocipath

import (
	"mime"
	"regexp"
)

// FallbackMediaType is served for any /v2/ body whose declared media type is
// not a registry type.
const FallbackMediaType = "application/octet-stream"

// registryTypeRE matches the JSON manifest/index/listing media types registries
// use: application/vnd.oci.image.manifest.v1+json,
// application/vnd.docker.distribution.manifest.v2+json, the signed schema 1
// +prettyjws, and so on.
var registryTypeRE = regexp.MustCompile(`^application/vnd\.[a-z0-9][a-z0-9.+-]*\+(json|prettyjws)$`)

// SafeMediaType returns raw without its parameters when it is a registry JSON
// media type or application/json. Anything else (text/html, image/svg+xml, a
// type smuggled in a manifest body, ...) is reported as not ok: manifest and
// listing types are uploader or upstream controlled and the /v2/ response is
// served from the application origin, so only known inert types may reach the
// Content-Type header.
func SafeMediaType(raw string) (string, bool) {
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}

	if mt == "application/json" || registryTypeRE.MatchString(mt) {
		return mt, true
	}

	return "", false
}

// ServedMediaType is SafeMediaType with FallbackMediaType for rejected types.
func ServedMediaType(raw string) string {
	if mt, ok := SafeMediaType(raw); ok {
		return mt
	}

	return FallbackMediaType
}
