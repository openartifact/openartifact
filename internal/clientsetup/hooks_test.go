// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package clientsetup

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype"
)

// TestMain registers the builder of every format, as the format plugin does at
// startup: Templates and Snippets dispatch on the registry.
func TestMain(m *testing.M) {
	for format, b := range map[string]Builder{
		"npm": NPMBuilder, "docker": DockerBuilder, "pypi": PyPIBuilder, "maven": MavenBuilder,
		"helm": HelmBuilder, "deb": DebBuilder, "rpm": RPMBuilder, "pacman": PacmanBuilder,
		"nix": NixBuilder, "cargo": CargoBuilder, "composer": ComposerBuilder, "go": GoBuilder,
		"nuget": NuGetBuilder, "rubygems": RubyGemsBuilder, "raw": RawBuilder, "conda": CondaBuilder,
		"conan": ConanBuilder, "apk": APKBuilder, "terraform": TerraformBuilder,
		"ansible": AnsibleBuilder, "sbt": SBTBuilder, "ivy": IvyBuilder,
	} {
		Register(format, b)
	}

	os.Exit(m.Run())
}

// TestBuildersGiveRealSnippets: a registered builder must write real client
// setup instructions for every repository type, not the bare-URL fallback.
func TestBuildersGiveRealSnippets(t *testing.T) {
	t.Parallel()

	for _, format := range builders.Names() {
		for _, repoType := range []string{repotype.Hosted, repotype.Proxy, repotype.Group} {
			snippets := Snippets(format, "https://oa.example.com", "my-repo", repoType)
			if len(snippets) == 0 {
				t.Fatalf("Snippets(%q, %q) is empty", format, repoType)
			}

			for _, s := range snippets {
				if s.Client == "" || s.Client == "URL" || s.Configure == "" {
					t.Errorf("Snippets(%q, %q) has a bare or incomplete entry: %+v", format, repoType, s)
				}
			}
		}
	}
}
