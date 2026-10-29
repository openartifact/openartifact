// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"os"
	"testing"
)

// TestMain registers the rule of every format once, the way
// internal/format/plugin does at startup, so the tests can dispatch by format.
func TestMain(m *testing.M) {
	for format, rule := range map[string]Rule{
		"npm":      NPMRule,
		"pypi":     PyPIRule,
		"maven":    MavenRule,
		"ivy":      IvyRule,
		"sbt":      SBTRule,
		"nuget":    NuGetRule,
		"cargo":    CargoRule,
		"composer": ComposerRule,
		"helm":     HelmRule,
		"deb":      DebRule,
		"rpm":      RPMRule,
		"pacman":   PacmanRule,
		"go":       GoRule,
		"rubygems": RubyGemsRule,
		"conda":    CondaRule,
		"conan":    ConanRule,
	} {
		Register(format, rule)
	}

	os.Exit(m.Run())
}
