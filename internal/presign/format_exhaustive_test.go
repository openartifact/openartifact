// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package presign

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestIsProxyBlobPathDecidesEveryFormat fails when a format is added to the
// registry without deciding whether its proxy downloads may be redirected.
// apk, ivy and sbt have no case: they fall to the default and are always
// streamed, as is docker (served by the registry, not by path).
func TestIsProxyBlobPathDecidesEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "presign.IsProxyBlobPath",
		[]string{
			"maven", "deb", "rpm", "pacman", "npm", "helm", "cargo", "go", "nuget", "rubygems",
			"conan", "conda", "terraform", "raw", "pypi", "nix",
		},
		[]string{"docker", "ivy", "sbt", "apk"})

	blobs := map[string]string{
		"maven":     "g/a/1.0/a-1.0.jar",
		"deb":       "pool/main/a/app/app_1_amd64.deb",
		"rpm":       "Packages/a-1.rpm",
		"pacman":    "x86_64/a-1-1-x86_64.pkg.tar.zst",
		"npm":       "left-pad/-/left-pad-1.0.0.tgz",
		"helm":      "charts/a-1.tgz",
		"cargo":     "api/v1/crates/a/1.0.0/download",
		"go":        "example.com/m/@v/v1.0.0.zip",
		"nuget":     "v3/package/a/1.0.0/a.1.0.0.nupkg",
		"rubygems":  "gems/a-1.gem",
		"conan":     "v2/conans/a/1/_/_/revisions/r/files/conan_package.tgz",
		"conda":     "linux-64/a-1.conda",
		"terraform": "providers/hashicorp/aws/1.0.0/linux_amd64/p.zip",
		"raw":       "any/file.bin",
		"pypi":      "files/x-1.whl",
		"nix":       "nar/0123456789abcdfghijklmnpqrsvwxyz0123456789abcdfghijk.nar.xz",
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		got := IsProxyBlobPath(format, blobs[format])

		if slices.Contains(handled, format) != got {
			t.Errorf("IsProxyBlobPath(%q, %q) = %v, want %v", format, blobs[format], got, !got)
		}
	})
}
