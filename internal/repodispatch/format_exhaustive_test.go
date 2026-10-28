// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"testing"

	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestRestUploadEndpointDecidesEveryFormat fails when a format is added to the
// registry without deciding whether it has a REST-only upload endpoint.
func TestRestUploadEndpointDecidesEveryFormat(t *testing.T) {
	t.Parallel()

	want := map[string]string{"deb": "debs", "rpm": "packages"}
	handled := repotypetest.Classified(t, "repodispatch.restUploadEndpoint",
		[]string{"deb", "rpm"},
		[]string{
			"ansible", "apk", "cargo", "composer", "conan", "conda", "docker", "go", "helm", "ivy", "maven", "nix", "npm",
			"nuget", "pacman", "pypi", "raw", "rubygems", "sbt", "terraform",
		})

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		got := restUploadEndpoint(format)
		isHandled := false

		for _, h := range handled {
			isHandled = isHandled || h == format
		}

		switch {
		case isHandled && got != want[format]:
			t.Errorf("restUploadEndpoint(%q) = %q, want %q", format, got, want[format])
		case !isHandled && got != "":
			t.Errorf("restUploadEndpoint(%q) = %q, want the empty fallthrough", format, got)
		}
	})
}
