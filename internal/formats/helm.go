// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	formathelm "github.com/openartifact/openartifact/internal/format/helm"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
)

// helmFormat is the registration of the helm format.
func helmFormat() plugin.Format {
	return plugin.Definition{
		ID: "helm",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formathelm.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.HelmRule,
			Deps:     &pkgdeps.HelmHooks,
		},
		Snippets: clientsetup.HelmBuilder,
		Mirror:   plugin.ProxyOf(proxy.HelmHooks),
		Merge:    plugin.GroupOf(&group.HelmHooks),
		Meta:     lifecycle.None,
	}
}
