// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocipath

import "testing"

func TestSafeMediaType(t *testing.T) {
	for in, want := range map[string]string{
		"application/vnd.oci.image.manifest.v1+json":                "application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json":      "application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.v1+prettyjws": "application/vnd.docker.distribution.manifest.v1+prettyjws",
		"application/vnd.oci.image.index.v1+json; charset=utf-8":    "application/vnd.oci.image.index.v1+json",
		"Application/JSON":               "application/json",
		"text/html":                      "application/octet-stream",
		"text/html; charset=utf-8":       "application/octet-stream",
		"image/svg+xml":                  "application/octet-stream",
		"application/xhtml+xml":          "application/octet-stream",
		"application/vnd.evil+xml":       "application/octet-stream",
		"application/vnd.a+json<script>": "application/octet-stream",
		"":                               "application/octet-stream",
		"application/vnd.docker.distribution.manifest.v2+json\nX: y": "application/octet-stream",
	} {
		if got := ServedMediaType(in); got != want {
			t.Errorf("ServedMediaType(%q) = %q, want %q", in, got, want)
		}
	}
}
