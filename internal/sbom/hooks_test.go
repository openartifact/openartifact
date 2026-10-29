// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"os"
	"testing"
)

// TestMain registers the SBOM hooks of every format that has any, as the format
// plugin does at startup: generatePURLNS and rootType dispatch on them.
func TestMain(m *testing.M) {
	for format, h := range map[string]Hooks{
		"maven": MavenHooks, "ivy": MavenHooks, "sbt": MavenHooks, "pypi": PyPIHooks,
		"pacman": PacmanHooks, "deb": DebHooks, "rpm": RPMHooks,
		"docker": DockerHooks, "raw": RawHooks,
	} {
		Register(format, h)
	}

	os.Exit(m.Run())
}
