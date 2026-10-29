// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatconda "github.com/openartifact/openartifact/internal/format/conda"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
)

// condaFormat is the registration of the conda format.
func condaFormat() plugin.Format {
	return plugin.Definition{
		ID: "conda",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatconda.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.CondaRule,
			Deps:     &pkgdeps.CondaHooks,
		},
		Snippets: clientsetup.CondaBuilder,
		Mirror:   plugin.ProxyOf(proxy.CondaHooks),
		Merge:    plugin.GroupOf(&group.CondaHooks),
		Meta:     lifecycle.None,
	}
}
