// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatpypi "github.com/openartifact/openartifact/internal/format/pypi"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// pypiFormat is the registration of the pypi format.
func pypiFormat() plugin.Format {
	return plugin.Definition{
		ID: "pypi",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatpypi.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.PyPIRule,
			Deps:     &pkgdeps.PyPIHooks,
			License:  &licensemeta.PyPIHooks,
			Vuln:     &vuln.PyPIHooks,
			SBOM:     &sbom.PyPIHooks,
		},
		Snippets: clientsetup.PyPIBuilder,
		Mirror:   plugin.ProxyOf(proxy.PyPIHooks),
		Merge:    plugin.GroupOf(&group.PyPIHooks),
		Meta:     lifecycle.None,
	}
}
