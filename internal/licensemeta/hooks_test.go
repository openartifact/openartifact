// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

// init registers the manifest rules and the license hooks of every format
// once, the way internal/format/plugin does at startup, so the tests can
// dispatch by format.
// It is an init, not a TestMain, so the fuzz targets registered by ClusterFuzzLite
// (which compiles the _test.go files into the fuzzer but never runs TestMain)
// see the same registry.
func init() { //nolint:gochecknoinits // test-only format registry, see above
	for format, rule := range map[string]manifest.Rule{
		"pypi":     manifest.PyPIRule,
		"npm":      manifest.NPMRule,
		"cargo":    manifest.CargoRule,
		"composer": manifest.ComposerRule,
		"nuget":    manifest.NuGetRule,
		"maven":    manifest.MavenRule,
		"ivy":      manifest.IvyRule,
		"sbt":      manifest.SBTRule,
		"rubygems": manifest.RubyGemsRule,
	} {
		manifest.Register(format, rule)
	}

	for format, h := range map[string]Hooks{
		"pypi":     PyPIHooks,
		"npm":      NPMHooks,
		"cargo":    CargoHooks,
		"composer": ComposerHooks,
		"nuget":    NuGetHooks,
		"maven":    MavenHooks,
		"ivy":      IvyHooks,
		"sbt":      SBTHooks,
		"rubygems": RubyGemsHooks,
	} {
		Register(format, h)
	}
}

// TestJVMExpectedIsThePOM guards the per-format decision that the .pom is the
// authority of a maven or sbt module: the jar beside it declares nothing.
func TestJVMExpectedIsThePOM(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"maven", "sbt"} {
		if !Expected(format, "x-1.0.pom") {
			t.Errorf("Expected(%q, .pom) = false", format)
		}

		if Expected(format, "x-1.0.jar") {
			t.Errorf("Expected(%q, .jar) = true: the pom is the authority", format)
		}

		if Supported(format, "x-1.0.xml") {
			t.Errorf("Supported(%q, .xml) = true: maven.xml files are ivy's", format)
		}
	}
}

// TestUnregisteredFormatIsUnsupported covers a format with no hooks.
func TestUnregisteredFormatIsUnsupported(t *testing.T) {
	t.Parallel()

	if Supported("docker", "x-1.0.pom") || Expected("docker", "x-1.0.pom") {
		t.Error("an unregistered format is supported or expected")
	}

	if _, err := ReadBytes(t.Context(), "docker", "x-1.0.pom", nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Read(docker) error = %v, want ErrUnsupported", err)
	}
}
