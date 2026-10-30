// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

// init registers the group hooks of every format this package has, the way
// internal/format/plugin does at startup: the handler serves only the formats
// registered. docker and raw have no hooks (plain first-hit groups).
// It is an init, not a TestMain, so the fuzz targets registered by ClusterFuzzLite
// (which compiles the _test.go files into the fuzzer but never runs TestMain)
// see the same registry.
func init() { //nolint:gochecknoinits // test-only format registry, see above
	for format, h := range map[string]Hooks{
		"ansible":   AnsibleHooks,
		"apk":       ApkHooks,
		"cargo":     CargoHooks,
		"composer":  ComposerHooks,
		"conan":     ConanHooks,
		"conda":     CondaHooks,
		"deb":       DebHooks,
		"go":        GoHooks,
		"helm":      HelmHooks,
		"ivy":       IvyHooks,
		"maven":     MavenHooks,
		"nix":       NixHooks,
		"npm":       NPMHooks,
		"nuget":     NuGetHooks,
		"pacman":    PacmanHooks,
		"pypi":      PyPIHooks,
		"rpm":       RPMHooks,
		"rubygems":  RubyGemsHooks,
		"sbt":       SBTHooks,
		"terraform": TerraformHooks,
	} {
		Register(format, h)
	}
}

// classifyDebRPM classifies a request path of a deb, pacman or rpm group.
func classifyDebRPM(format, relPath string) debRPMPath {
	switch format {
	case "deb":
		return debFormat.classifyPath(relPath)
	case "pacman":
		return pacmanFormat.classifyPath(relPath)
	}

	return rpmFormat.classifyPath(relPath)
}
