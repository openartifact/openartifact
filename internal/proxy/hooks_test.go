// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

// init registers the hooks of every proxy format, as plugin.Register does
// at startup in the server; tests dispatch by format through them.
// It is an init, not a TestMain, so the fuzz targets registered by ClusterFuzzLite
// (which compiles the _test.go files into the fuzzer but never runs TestMain)
// see the same registry.
func init() { //nolint:gochecknoinits // test-only format registry, see above
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
}
