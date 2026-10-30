// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

// init registers the rule of every format once, the way
// internal/format/plugin does at startup, so the tests can dispatch by format.
// It is an init, not a TestMain, so the fuzz targets registered by ClusterFuzzLite
// (which compiles the _test.go files into the fuzzer but never runs TestMain)
// see the same registry.
func init() { //nolint:gochecknoinits // test-only format registry, see above
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
}
