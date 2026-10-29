// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	formatpacman "github.com/openartifact/openartifact/internal/format/pacman"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
)

// pacmanFormat is the registration of the pacman format.
func pacmanFormat() plugin.Format {
	return plugin.Definition{
		ID: "pacman",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := formatpacman.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL)
			if regen, ok := lifecycle.Worker[formatpacman.Regenerator](d.Workers, "pacman"); ok {
				h.SetRegenerator(regen)
			}

			return h, nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.PacmanRule,
			Deps:     &pkgdeps.PacmanHooks,
			SBOM:     &sbom.PacmanHooks,
		},
		Snippets: clientsetup.PacmanBuilder,
		Mirror:   plugin.ProxyOf(proxy.PacmanHooks),
		Merge:    plugin.GroupOf(&group.PacmanHooks),
		Meta:     repoLifecycle("pacman", false),
	}
}
