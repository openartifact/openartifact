// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"net/http"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestFormatServerDecidesEveryFormat fails when a format is added to the
// registry without deciding whether it has its own serve handler.
func TestFormatServerDecidesEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "proxy.formatServer",
		[]string{"pypi", "npm", "helm", "cargo", "go", "nuget", "rubygems", "terraform"},
		[]string{"docker", "maven", "deb", "rpm", "raw", "conda", "conan", "ivy", "sbt", "apk", "pacman", "nix", "ansible"})

	s := &Service{}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if got := s.formatServer(format) != nil; got != slices.Contains(handled, format) {
			t.Errorf("formatServer(%q) non-nil = %v, classified handled = %v", format, got, !got)
		}
	})
}

// TestFetchDecidesEveryFormat fails when a format is added to the registry
// without deciding whether Service.fetch derives per-format fetch options.
// Classified only: the options need a database and an upstream to probe.
func TestFetchDecidesEveryFormat(t *testing.T) {
	t.Parallel()

	repotypetest.Classified(t, "proxy.Service.fetch",
		[]string{"ivy", "sbt", "apk", "nix"},
		[]string{
			"docker", "npm", "pypi", "maven", "helm", "cargo", "rubygems", "deb", "rpm", "nuget", "go",
			"raw", "conda", "conan", "terraform", "pacman", "ansible",
		})

	for _, f := range []string{"ivy", "sbt"} {
		if !isIvyFormat(f) {
			t.Errorf("isIvyFormat(%q) = false", f)
		}
	}
}

// TestImmutableDecidesEveryFormat fails when a format is added to the registry
// without deciding which of its keys are cached as immutable. pacman and raw
// have no rule: their files use the repository TTL.
func TestImmutableDecidesEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "proxy.Immutable",
		[]string{
			"maven", "deb", "rpm", "pypi", "npm", "helm", "cargo", "go", "nuget", "rubygems",
			"conda", "docker", "apk", "ivy", "sbt", "conan", "terraform", "nix",
		},
		[]string{"raw", "pacman", "ansible"})

	keys := map[string]string{
		"maven":     "g/a/1.0/a-1.0.jar",
		"deb":       "pool/main/a/app/app_1_amd64.deb",
		"rpm":       "Packages/a-1.rpm",
		"pypi":      "files/x-1.whl",
		"npm":       "left-pad/-/left-pad-1.0.0.tgz",
		"helm":      "charts/a-1.tgz",
		"cargo":     "api/v1/crates/a/1.0.0/download",
		"go":        "example.com/m/@v/v1.0.0.zip",
		"nuget":     "v3/package/a/1.0.0/a.1.0.0.nupkg",
		"rubygems":  "gems/a-1.gem",
		"conda":     "linux-64/a-1.conda",
		"docker":    "v2/x/blobs/sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"apk":       "v3.20/main/x86_64/curl-8.0.0-r0.apk",
		"ivy":       "org/a/1.0/a-1.0.jar",
		"sbt":       "org/a/1.0/a-1.0.jar",
		"conan":     "v2/conans/a/1/_/_/revisions/r/files/conanfile.py",
		"terraform": "providers/hashicorp/aws/1.0.0/linux_amd64/p.zip",
		"raw":       "any/file.bin",
		"pacman":    "x86_64/a-1-1-x86_64.pkg.tar.zst",
		"nix":       "nar/0123456789abcdfghijklmnpqrsvwxyz0123456789abcdfghijk.nar.xz",
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if got := Immutable(format, keys[format]); got != slices.Contains(handled, format) {
			t.Errorf("Immutable(%q, %q) = %v, want %v", format, keys[format], got, !got)
		}
	})
}

// TestIsMetadataKeyDecidesEveryFormat fails when a format is added to the
// registry without deciding which of its keys are capped metadata. docker
// (manifests are sized by the OCI path) and raw (no index files) have no rule.
func TestIsMetadataKeyDecidesEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "proxy.isMetadataKey",
		[]string{
			"deb", "rpm", "maven", "apk", "ivy", "sbt", "pypi", "npm", "helm", "cargo", "nuget", "rubygems",
			"go", "conda", "conan", "terraform", "pacman", "nix",
		},
		[]string{"docker", "raw", "ansible"})

	keys := map[string]string{
		"deb":       "dists/stable/InRelease",
		"rpm":       "repodata/repomd.xml",
		"maven":     "g/a/maven-metadata.xml",
		"apk":       "v3.20/main/x86_64/APKINDEX.tar.gz",
		"ivy":       "org/a/maven-metadata.xml",
		"sbt":       "org/a/maven-metadata.xml",
		"pypi":      "simple/requests/",
		"npm":       "left-pad",
		"helm":      "index.yaml",
		"cargo":     "config.json",
		"nuget":     "v3/index.json",
		"rubygems":  "versions",
		"go":        "example.com/m/@v/list",
		"conda":     "linux-64/repodata.json",
		"conan":     "v2/conans/a/1/_/_/latest",
		"terraform": ".well-known/terraform.json",
		"pacman":    "x86_64/core.db",
		"docker":    "v2/x/manifests/latest",
		"raw":       "index.html",
		"nix":       "nix-cache-info",
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if got := isMetadataKey(format, keys[format]); got != slices.Contains(handled, format) {
			t.Errorf("isMetadataKey(%q, %q) = %v, want %v", format, keys[format], got, !got)
		}
	})
}

// TestPackageDirDecidesEveryFormat fails when a format is added to the registry
// without deciding whether it keeps package files under a fixed prefix.
// packageDir is only consulted for cargo, nuget and rubygems; rubygems is the
// function's default, every other format is not meant to reach it.
func TestPackageDirDecidesEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "proxy.packageDir",
		[]string{"cargo", "nuget", "rubygems"},
		[]string{
			"docker", "npm", "pypi", "maven", "helm", "deb", "rpm", "go", "raw", "conda", "conan", "ivy", "sbt",
			"terraform", "apk", "pacman", "nix", "ansible",
		})

	want := map[string]string{"cargo": "api/v1/crates/", "nuget": "v3/package/", "rubygems": "gems/"}

	for _, format := range handled {
		if got := packageDir(format); got != want[format] {
			t.Errorf("packageDir(%q) = %q, want %q", format, got, want[format])
		}
	}
}

// TestPackageExtsDecideEveryFormat fails when a format is added to the registry
// without deciding whether an HTML answer for its package files is refused.
func TestPackageExtsDecideEveryFormat(t *testing.T) {
	t.Parallel()

	ignored := []string{
		"docker", "npm", "pypi", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conan", "apk", "ansible",
	}

	for _, site := range []string{"proxy.packageExts", "proxy.isHTMLForPackage"} {
		repotypetest.Classified(t, site,
			[]string{"maven", "deb", "rpm", "pacman", "ivy", "sbt", "conda", "terraform", "nix"}, ignored)
	}

	html := &http.Response{Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}}}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		exts := packageExts[format]
		if want := !slices.Contains(ignored, format); (len(exts) > 0) != want {
			t.Errorf("packageExts[%q] has %d extensions, want non-empty = %v", format, len(exts), want)
		}

		for _, ext := range exts {
			if !isHTMLForPackage(format, "some/file"+ext, html) {
				t.Errorf("isHTMLForPackage(%q, file%s) = false, want true", format, ext)
			}
		}

		if isHTMLForPackage(format, "some/file.unknown-ext", html) {
			t.Errorf("isHTMLForPackage(%q) refused a path with no package extension", format)
		}
	})
}
