// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// mavenFormat is the registration of the maven format.
func mavenFormat() plugin.Format {
	return plugin.Definition{
		ID: "maven",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return withREST(formatmaven.NewHTTPHandler(d.Log, d.DB, d.Served), d, "maven"), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.MavenRule,
			Deps:     &pkgdeps.MavenHooks,
			License:  &licensemeta.MavenHooks,
			Vuln:     &vuln.MavenHooks,
			SBOM:     &sbom.MavenHooks,
		},
		Snippets: clientsetup.MavenBuilder,
		Mirror:   plugin.ProxyOf(proxy.MavenHooks),
		Merge:    plugin.GroupOf(&group.MavenHooks),
		Meta:     mavenLifecycle(),
	}
}
