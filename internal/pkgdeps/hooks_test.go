// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// TestMain registers the manifest rules and the dependency hooks of every
// format once, the way internal/format/plugin does at startup, so the tests can
// dispatch by format.
func TestMain(m *testing.M) {
	for format, rule := range map[string]manifest.Rule{
		"npm":      manifest.NPMRule,
		"pypi":     manifest.PyPIRule,
		"maven":    manifest.MavenRule,
		"ivy":      manifest.IvyRule,
		"sbt":      manifest.SBTRule,
		"nuget":    manifest.NuGetRule,
		"cargo":    manifest.CargoRule,
		"composer": manifest.ComposerRule,
		"helm":     manifest.HelmRule,
		"deb":      manifest.DebRule,
		"rpm":      manifest.RPMRule,
		"pacman":   manifest.PacmanRule,
		"go":       manifest.GoRule,
		"rubygems": manifest.RubyGemsRule,
		"conda":    manifest.CondaRule,
		"conan":    manifest.ConanRule,
	} {
		manifest.Register(format, rule)
	}

	for format, h := range map[string]Hooks{
		"npm":      NPMHooks,
		"pypi":     PyPIHooks,
		"maven":    MavenHooks,
		"ivy":      IvyHooks,
		"sbt":      SBTHooks,
		"nuget":    NuGetHooks,
		"cargo":    CargoHooks,
		"composer": ComposerHooks,
		"helm":     HelmHooks,
		"deb":      DebHooks,
		"rpm":      RPMHooks,
		"pacman":   PacmanHooks,
		"go":       GoHooks,
		"rubygems": RubyGemsHooks,
		"conda":    CondaHooks,
		"conan":    ConanHooks,
	} {
		Register(format, h)
	}

	os.Exit(m.Run())
}

// TestSBTReadsLikeMaven guards the per-format decision that sbt takes maven's
// manifest rule (a .xml file is ivy's, not sbt's).
func TestSBTReadsLikeMaven(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		format, file string
		want         bool
	}{
		{"sbt", "x-1.0.pom", true},
		{"sbt", "ivy.xml", false},
		{"maven", "ivy.xml", false},
		{"ivy", "ivy.xml", true},
		{"docker", "x-1.0.pom", false},
	} {
		if got := Supported(tc.format, tc.file); got != tc.want {
			t.Errorf("Supported(%q, %q) = %v, want %v", tc.format, tc.file, got, tc.want)
		}
	}
}
