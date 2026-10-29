// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// npmFormat is the registration of the npm format.
func npmFormat() plugin.Format {
	return plugin.Definition{
		ID: "npm",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := npmHosted{formatnpm.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL)}

			publish, ok := plugin.REST[npmPublisher](d, "npm")
			if !ok {
				return h, nil
			}

			return npmPublishing{npmHosted: h, publish: publish}, nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.NPMRule,
			Deps:     &pkgdeps.NPMHooks,
			License:  &licensemeta.NPMHooks,
			Vuln:     &vuln.NPMHooks,
		},
		Snippets: clientsetup.NPMBuilder,
		Mirror:   plugin.ProxyOf(proxy.NPMHooks),
		Merge:    plugin.GroupOf(&group.NPMHooks),
		Meta:     npmLifecycle(),
	}
}
