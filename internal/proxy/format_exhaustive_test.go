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
		[]string{"ansible", "cargo", "go", "helm", "npm", "nuget", "pypi", "rubygems", "terraform"},
		[]string{"apk", "conan", "conda", "deb", "docker", "ivy", "maven", "nix", "pacman", "raw", "rpm", "sbt"})

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
		[]string{"apk", "ivy", "nix", "sbt"},
		[]string{
			"ansible", "cargo", "conan", "conda", "deb", "docker", "go", "helm", "maven", "npm", "nuget",
			"pacman", "pypi", "raw", "rpm", "rubygems", "terraform",
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
			"ansible", "apk", "cargo", "conan", "conda", "deb", "docker", "go", "helm", "ivy", "maven", "nix", "npm",
			"nuget", "pypi", "rpm", "rubygems", "sbt", "terraform",
		},
		[]string{"pacman", "raw"})

	keys := map[string]string{
		"ansible":   "api/v3/artifacts/collections/a-b-1.0.0.tar.gz",
		"apk":       "v3.20/main/x86_64/curl-8.0.0-r0.apk",
		"cargo":     "api/v1/crates/a/1.0.0/download",
		"conan":     "v2/conans/a/1/_/_/revisions/r/files/conanfile.py",
		"conda":     "linux-64/a-1.conda",
		"deb":       "pool/main/a/app/app_1_amd64.deb",
		"docker":    "v2/x/blobs/sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"go":        "example.com/m/@v/v1.0.0.zip",
		"helm":      "charts/a-1.tgz",
		"ivy":       "org/a/1.0/a-1.0.jar",
		"maven":     "g/a/1.0/a-1.0.jar",
		"nix":       "nar/0123456789abcdfghijklmnpqrsvwxyz0123456789abcdfghijk.nar.xz",
		"npm":       "left-pad/-/left-pad-1.0.0.tgz",
		"nuget":     "v3/package/a/1.0.0/a.1.0.0.nupkg",
		"pacman":    "x86_64/a-1-1-x86_64.pkg.tar.zst",
		"pypi":      "files/x-1.whl",
		"raw":       "any/file.bin",
		"rpm":       "Packages/a-1.rpm",
		"rubygems":  "gems/a-1.gem",
		"sbt":       "org/a/1.0/a-1.0.jar",
		"terraform": "providers/hashicorp/aws/1.0.0/linux_amd64/p.zip",
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
			"ansible", "apk", "cargo", "conan", "conda", "deb", "go", "helm", "ivy", "maven", "nix", "npm", "nuget",
			"pacman", "pypi", "rpm", "rubygems", "sbt", "terraform",
		},
		[]string{"docker", "raw"})

	keys := map[string]string{
		"ansible":   "api/v3/collections/a/b/versions/",
		"apk":       "v3.20/main/x86_64/APKINDEX.tar.gz",
		"cargo":     "config.json",
		"conan":     "v2/conans/a/1/_/_/latest",
		"conda":     "linux-64/repodata.json",
		"deb":       "dists/stable/InRelease",
		"docker":    "v2/x/manifests/latest",
		"go":        "example.com/m/@v/list",
		"helm":      "index.yaml",
		"ivy":       "org/a/maven-metadata.xml",
		"maven":     "g/a/maven-metadata.xml",
		"nix":       "nix-cache-info",
		"npm":       "left-pad",
		"nuget":     "v3/index.json",
		"pacman":    "x86_64/core.db",
		"pypi":      "simple/requests/",
		"raw":       "index.html",
		"rpm":       "repodata/repomd.xml",
		"rubygems":  "versions",
		"sbt":       "org/a/maven-metadata.xml",
		"terraform": ".well-known/terraform.json",
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
			"ansible", "apk", "conan", "conda", "deb", "docker", "go", "helm", "ivy", "maven", "nix", "npm", "pacman",
			"pypi", "raw", "rpm", "sbt", "terraform",
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
		"ansible", "apk", "cargo", "conan", "docker", "go", "helm", "npm", "nuget", "pypi", "raw", "rubygems",
	}

	for _, site := range []string{"proxy.packageExts", "proxy.isHTMLForPackage"} {
		repotypetest.Classified(t, site,
			[]string{"conda", "deb", "ivy", "maven", "nix", "pacman", "rpm", "sbt", "terraform"}, ignored)
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
