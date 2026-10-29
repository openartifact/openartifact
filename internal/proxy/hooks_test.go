// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"os"
	"testing"
)

// TestMain registers the hooks of every proxy format, as plugin.Register does
// at startup in the server; tests dispatch by format through them.
func TestMain(m *testing.M) {
	for format, h := range map[string]Hooks{
		"ansible":   AnsibleHooks,
		"apk":       APKHooks,
		"cargo":     CargoHooks,
		"composer":  ComposerHooks,
		"conan":     ConanHooks,
		"conda":     CondaHooks,
		"deb":       DebHooks,
		"docker":    DockerHooks,
		"go":        GoHooks,
		"helm":      HelmHooks,
		"ivy":       IvyHooks,
		"maven":     MavenHooks,
		"nix":       NixHooks,
		"npm":       NPMHooks,
		"nuget":     NuGetHooks,
		"pacman":    PacmanHooks,
		"pypi":      PyPIHooks,
		"raw":       RawHooks,
		"rpm":       RPMHooks,
		"rubygems":  RubyGemsHooks,
		"sbt":       SbtHooks,
		"terraform": TerraformHooks,
	} {
		Register(format, h)
	}

	os.Exit(m.Run())
}
