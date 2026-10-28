// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestRootTypeCoversEveryFormat: the CycloneDX component type of the root of an
// SBOM; every format that is neither an image nor a bare file is a library.
func TestRootTypeCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "sbom.rootType",
		[]string{"docker", "raw"},
		[]string{
			"ansible", "apk", "cargo", "composer", "conan", "conda", "deb", "go", "helm", "ivy", "maven", "nix", "npm", "nuget",
			"pacman", "pypi", "rpm", "rubygems", "sbt", "terraform",
		},
	)

	want := map[string]string{"docker": "container", "raw": "file"}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		expected := "library"
		if slices.Contains(handled, format) {
			expected = want[format]
		}

		if got := rootType(format); got != expected {
			t.Errorf("rootType(%s) = %q, want %q", format, got, expected)
		}
	})
}

// TestGeneratePURLNameRulesCoverEveryFormat: the formats whose Package URL
// rewrites the package name (maven coordinates, pypi normalization, the
// pacman distro segment); the others keep the name as given.
func TestGeneratePURLNameRulesCoverEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "sbom.generatePURLNS",
		[]string{"ivy", "maven", "pacman", "pypi", "sbt"},
		[]string{
			"ansible", "apk", "cargo", "composer", "conan", "conda", "deb", "docker", "go", "helm", "nix", "npm", "nuget",
			"raw", "rpm", "rubygems", "terraform",
		},
	)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		got := generatePURL(format, "Org_X:Lib", "1.0")
		plain := "pkg:" + purlType(format) + "/" + purlPath("Org_X:Lib") + "@1.0"
		rewritten := got != plain

		if rewritten != slices.Contains(handled, format) {
			t.Errorf("generatePURL(%s) = %q: name rewritten = %v, handled = %v", format, got, rewritten, !rewritten)
		}
	})
}
