// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// rpmFormat is the registration of the rpm format.
func rpmFormat() plugin.Format {
	return plugin.Definition{
		ID: "rpm",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return withREST(quiet{formatrpm.NewHTTPHandler(d.Log, d.DB, d.Served)}, d, "rpm"), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.RPMRule,
			Deps:     &pkgdeps.RPMHooks,
			Vuln:     &vuln.RPMHooks,
			SBOM:     &sbom.RPMHooks,
		},
		Snippets: clientsetup.RPMBuilder,
		Mirror:   plugin.ProxyOf(proxy.RPMHooks),
		Merge:    plugin.GroupOf(&group.RPMHooks),
		Meta:     repoLifecycle("rpm", true),
	}
}
