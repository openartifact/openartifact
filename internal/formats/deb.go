// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// debFormat is the registration of the deb format.
func debFormat() plugin.Format {
	return plugin.Definition{
		ID: "deb",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return withREST(quiet{formatdeb.NewHTTPHandler(d.Log, d.DB, d.Served)}, d, "deb"), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.DebRule,
			Deps:     &pkgdeps.DebHooks,
			Vuln:     &vuln.DebHooks,
			SBOM:     &sbom.DebHooks,
		},
		Snippets: clientsetup.DebBuilder,
		Mirror:   plugin.ProxyOf(proxy.DebHooks),
		Merge:    plugin.GroupOf(&group.DebHooks),
		Meta:     repoLifecycle("deb", false),
	}
}
