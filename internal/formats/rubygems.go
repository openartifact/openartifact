// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatrubygems "github.com/openartifact/openartifact/internal/format/rubygems"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// rubygemsFormat is the registration of the rubygems format.
func rubygemsFormat() plugin.Format {
	return plugin.Definition{
		ID: "rubygems",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatrubygems.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.RubyGemsRule,
			Deps:     &pkgdeps.RubyGemsHooks,
			License:  &licensemeta.RubyGemsHooks,
			Vuln:     &vuln.RubyGemsHooks,
		},
		Snippets: clientsetup.RubyGemsBuilder,
		Mirror:   plugin.ProxyOf(proxy.RubyGemsHooks),
		Merge:    plugin.GroupOf(&group.RubyGemsHooks),
		Meta:     lifecycle.None,
	}
}
