// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

func TestRuleForCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(
		t,
		"manifest.ruleFor",
		[]string{
			"npm",
			"pypi",
			"maven",
			"ivy",
			"sbt",
			"nuget",
			"cargo",
			"helm",
			"deb",
			"rpm",
			"pacman",
			"go",
			"rubygems",
			"conda",
			"conan",
		},
		// No manifest is extracted for these: docker images are layers, apk and raw carry no descriptor this
		// package reads, terraform modules/providers are not parsed, and a nix store path has no descriptor file.
		[]string{"docker", "apk", "raw", "terraform", "nix", "ansible"},
	)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		rule, ok := ruleFor(format)
		if want := slices.Contains(handled, format); ok != want {
			t.Fatalf("ruleFor(%q) ok = %v, want %v", format, ok, want)
		}

		if ok && rule.extract == nil {
			t.Errorf("ruleFor(%q) has no extractor", format)
		}
	})
}

func TestAutoEligibleFormats(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "manifest.AutoEligible",
		[]string{"maven", "ivy", "sbt"},
		[]string{
			"docker", "npm", "pypi", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan", "terraform", "deb", "rpm", "apk",
			"pacman", "nix", "ansible",
		})

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if !slices.Contains(handled, format) {
			// The default is Supported: any file name, including one no rule accepts.
			for _, name := range []string{"x.tgz", "x.jar", "x.pom", "x.bin"} {
				if got, want := AutoEligible(format, name), Supported(format, name); got != want {
					t.Errorf("AutoEligible(%q, %q) = %v, want Supported = %v", format, name, got, want)
				}
			}

			return
		}

		// One upload of a multi-file format yields one analysis: only the descriptor is eligible.
		if !AutoEligible(format, "dir/lib-1.0.pom") {
			t.Errorf("AutoEligible(%q, pom) = false", format)
		}

		if AutoEligible(format, "dir/lib-1.0.jar") {
			t.Errorf("AutoEligible(%q, jar) = true", format)
		}

		if !AutoEligible(format, "dir/ivy.xml") {
			t.Errorf("AutoEligible(%q, ivy.xml) = false", format)
		}
	})
}
