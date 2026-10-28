// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestWriteManifestCoversEveryFormat: the ecosystems trivy gets a lockfile
// (or manifest) for, by registry format name. deb and rpm are scanned from an
// SBOM instead; the other formats have no trivy lockfile format.
func TestWriteManifestCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "vuln.TrivyScanner.writeManifest",
		[]string{"cargo", "composer", "go", "maven", "npm", "nuget", "pypi", "rubygems"},
		[]string{
			"ansible", "apk", "conan", "conda", "deb", "docker", "helm", "ivy", "nix", "pacman", "raw", "rpm", "sbt", "terraform",
		},
	)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		dir := t.TempDir()
		s := &TrivyScanner{}
		err := s.writeManifest(dir, "pkg", "1.0.0", format)

		entries, _ := os.ReadDir(dir)

		if slices.Contains(handled, format) {
			if err != nil || len(entries) != 1 {
				t.Errorf("writeManifest(%s) = %v with %d files, want one lockfile", format, err, len(entries))
			}

			return
		}

		if !errors.Is(err, errUnsupportedEcosystem) || len(entries) != 0 {
			t.Errorf("writeManifest(%s) = %v with %d files, want errUnsupportedEcosystem and no file", format, err, len(entries))
		}
	})
}

// TestBuildPURLCoversEveryFormat: a grype-scannable format yields a Package
// URL of its registry type, the others are refused. handled have a name or
// qualifier rewrite of their own; the ignored grype formats (cargo, nuget,
// rubygems) use the plain name.
func TestBuildPURLCoversEveryFormat(t *testing.T) {
	t.Parallel()

	repotypetest.Classified(t, "vuln.buildPURL",
		[]string{"composer", "deb", "go", "maven", "npm", "pypi", "rpm"},
		[]string{
			"ansible",
			"apk",
			"cargo",
			"conan",
			"conda",
			"docker",
			"helm",
			"ivy",
			"nix",
			"nuget",
			"pacman",
			"raw",
			"rubygems",
			"sbt",
			"terraform",
		},
	)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		spec, _ := repotype.Lookup(format)
		got, err := buildPURL(Package{Ecosystem: format, Name: "group:pkg", Version: "1.0.0", Distro: "debian-12"})

		if !spec.GrypeScan {
			if !errors.Is(err, errUnsupportedEcosystem) {
				t.Errorf("buildPURL(%s) = %q, %v, want errUnsupportedEcosystem", format, got, err)
			}

			return
		}

		if err != nil || !strings.HasPrefix(got, "pkg:"+spec.PURLType+"/") {
			t.Errorf("buildPURL(%s) = %q, %v, want a pkg:%s/ URL", format, got, err, spec.PURLType)
		}
	})
}
