// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	formatnix "github.com/openartifact/openartifact/internal/format/nix"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
)

// nixFormat is the registration of the nix format.
func nixFormat() plugin.Format {
	return plugin.Definition{
		ID: "nix",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := formatnix.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL).WithMetrics(d.Metrics)
			if d.NixKeys != nil {
				h.WithKeys(d.NixKeys)
			}

			return h, nil
		},
		Supply:   plugin.NoAnalysis,
		Snippets: clientsetup.NixBuilder,
		Mirror:   plugin.ProxyOf(proxy.NixHooks),
		Merge:    plugin.GroupOf(&group.NixHooks),
		Meta:     lifecycle.None,
	}
}
