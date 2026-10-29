// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"os"
	"testing"
)

// TestMain registers the scanner hooks of every format that has any, as the
// format plugin does at startup: buildPURL and writeManifest dispatch on them.
func TestMain(m *testing.M) {
	for format, h := range map[string]Hooks{
		"npm": NPMHooks, "pypi": PyPIHooks, "maven": MavenHooks, "cargo": CargoHooks,
		"composer": ComposerHooks, "go": GoHooks, "nuget": NuGetHooks, "rubygems": RubyGemsHooks,
		"deb": DebHooks, "rpm": RPMHooks,
	} {
		Register(format, h)
	}

	os.Exit(m.Run())
}
