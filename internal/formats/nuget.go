// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	formatnuget "github.com/openartifact/openartifact/internal/format/nuget"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// nugetFormat is the registration of the nuget format.
func nugetFormat() plugin.Format {
	return plugin.Definition{
		ID: "nuget",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatnuget.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.NuGetRule,
			Deps:     &pkgdeps.NuGetHooks,
			License:  &licensemeta.NuGetHooks,
			Vuln:     &vuln.NuGetHooks,
		},
		Snippets: clientsetup.NuGetBuilder,
		Mirror:   plugin.ProxyOf(proxy.NuGetHooks),
		Merge:    plugin.GroupOf(&group.NuGetHooks),
		Meta:     lifecycle.None,
	}
}
