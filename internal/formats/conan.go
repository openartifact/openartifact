// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatconan "github.com/openartifact/openartifact/internal/format/conan"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
)

// conanFormat is the registration of the conan format.
func conanFormat() plugin.Format {
	return plugin.Definition{
		ID: "conan",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatconan.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.ConanRule,
			Deps:     &pkgdeps.ConanHooks,
		},
		Snippets: clientsetup.ConanBuilder,
		Mirror:   plugin.ProxyOf(proxy.ConanHooks),
		Merge:    plugin.GroupOf(&group.ConanHooks),
		Meta:     lifecycle.None,
	}
}
