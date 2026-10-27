// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"cmp"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
	"github.com/openartifact/openartifact/internal/vuln"
)

// TestWithDistroCoversEveryFormat: deb and rpm packages get the distribution of
// their release for the scanners that match by distro; the formats are the ones
// vuln.NeedsDistro names. A non-stored artifact id keeps the probe off the
// database.
func TestWithDistroCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "api.VulnHandler.withDistro",
		[]string{"deb", "rpm"},
		[]string{
			"docker", "npm", "pypi", "maven", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan",
			"ivy", "sbt", "terraform", "apk", "pacman",
		},
	)

	versions := map[string]string{"deb": "1.0-1+deb12u1", "rpm": "1.0-1.el9"}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if vuln.NeedsDistro(format) != slices.Contains(handled, format) {
			t.Errorf("withDistro(%s): vuln.NeedsDistro disagrees with the branches", format)
		}

		h := &VulnHandler{}

		got, err := h.withDistro(t.Context(), "repo", "not-a-uuid", vuln.Package{Ecosystem: format, Name: "p", Version: versions[format]})
		if err != nil {
			t.Fatal(err)
		}

		if (got.Distro != "") != slices.Contains(handled, format) {
			t.Errorf("withDistro(%s).Distro = %q, handled = %v", format, got.Distro, slices.Contains(handled, format))
		}
	})
}

// TestDownloadFilenameCoversEveryFormat: docker tags download their manifest as
// <name>.json and conan references are flattened; every other format keeps the
// base name of its file.
func TestDownloadFilenameCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "api.downloadFilename",
		[]string{"docker", "conan"},
		[]string{
			"npm", "pypi", "maven", "helm", "cargo", "rubygems", "deb", "rpm", "nuget", "go", "raw", "conda", "ivy", "sbt",
			"terraform", "apk", "pacman",
		},
	)

	want := map[string]string{"docker": "a_b.json", "conan": "a_b"}
	in := map[string]string{"docker": "a:b"}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		name := cmp.Or(in[format], "a/b")
		got := downloadFilename(&models.ComponentBlob{Format: format, Filename: name})

		if slices.Contains(handled, format) {
			if got != want[format] {
				t.Errorf("downloadFilename(%s) = %q, want %q", format, got, want[format])
			}

			return
		}

		if got != "b" {
			t.Errorf("downloadFilename(%s) = %q, want the base name %q", format, got, "b")
		}
	})
}
