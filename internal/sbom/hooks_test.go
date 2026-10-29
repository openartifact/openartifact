// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
)

// TestMain registers the SBOM hooks of every format that has any, as the format
// plugin does at startup: generatePURLNS and rootType dispatch on them. The
// package-file components also read the manifest rules, dependency hooks and
// license hooks of the formats the tests build packages for.
func TestMain(m *testing.M) {
	for format, h := range map[string]Hooks{
		"maven": MavenHooks, "ivy": MavenHooks, "sbt": MavenHooks, "pypi": PyPIHooks,
		"pacman": PacmanHooks, "deb": DebHooks, "rpm": RPMHooks,
		"docker": DockerHooks, "raw": RawHooks,
	} {
		Register(format, h)
	}

	for format, rule := range map[string]manifest.Rule{
		"npm": manifest.NPMRule, "maven": manifest.MavenRule, "deb": manifest.DebRule, "pypi": manifest.PyPIRule,
		"cargo": manifest.CargoRule, "go": manifest.GoRule, "rpm": manifest.RPMRule, "nuget": manifest.NuGetRule,
	} {
		manifest.Register(format, rule)
	}

	for format, h := range map[string]pkgdeps.Hooks{
		"npm": pkgdeps.NPMHooks, "maven": pkgdeps.MavenHooks, "deb": pkgdeps.DebHooks, "pypi": pkgdeps.PyPIHooks,
		"cargo": pkgdeps.CargoHooks, "go": pkgdeps.GoHooks, "rpm": pkgdeps.RPMHooks, "nuget": pkgdeps.NuGetHooks,
	} {
		pkgdeps.Register(format, h)
	}

	for format, h := range map[string]licensemeta.Hooks{
		"npm": licensemeta.NPMHooks, "maven": licensemeta.MavenHooks, "pypi": licensemeta.PyPIHooks,
		"cargo": licensemeta.CargoHooks, "nuget": licensemeta.NuGetHooks,
	} {
		licensemeta.Register(format, h)
	}

	os.Exit(m.Run())
}
