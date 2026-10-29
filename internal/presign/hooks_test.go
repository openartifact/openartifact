// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package presign

import (
	"os"
	"testing"
)

// TestMain registers the blob-path classifier of every proxy format that has
// one, as plugin.Register does at startup in the server.
func TestMain(m *testing.M) {
	for format, blobPath := range map[string]func(string) bool{
		"ansible":   AnsibleBlobPath,
		"cargo":     CargoBlobPath,
		"composer":  ComposerBlobPath,
		"conan":     ConanBlobPath,
		"conda":     CondaBlobPath,
		"deb":       DebBlobPath,
		"go":        GoBlobPath,
		"helm":      HelmBlobPath,
		"maven":     MavenBlobPath,
		"nix":       NixBlobPath,
		"npm":       NPMBlobPath,
		"nuget":     NuGetBlobPath,
		"pacman":    PacmanBlobPath,
		"pypi":      PyPIBlobPath,
		"raw":       RawBlobPath,
		"rpm":       RPMBlobPath,
		"rubygems":  RubyGemsBlobPath,
		"terraform": TerraformBlobPath,
	} {
		Register(format, blobPath)
	}

	os.Exit(m.Run())
}
