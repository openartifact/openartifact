// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatrubygems "github.com/openartifact/openartifact/internal/format/rubygems"
	"github.com/openartifact/openartifact/internal/format/storage"
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
		Store:    rubygemsStorage(),
	}
}

// rubygemsStorage describes how the generic engines treat rubygems rows: the same
// gem version in another platform is the same version for retention.
func rubygemsStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:     string(models.TrashKindRubyGems),
			NameExpr: "p.name || CASE WHEN f.arch IN ('', 'ruby') THEN '' ELSE ' (' || f.arch || ')' END",
		},
		Cleanup: storage.Cleanup{VersionExpr: keyHead},
		Detail: storage.Detail{
			Summary: fromV("summary"), Description: fromV("description"), Homepage: fromV("homepage"), License: fromV("licenses"),
			Info: []storage.InfoField{
				entry("authors", fromV("authors")), entry("platform", fromK("arch")), entry("yanked", fromV("yanked")),
				entry("download_count", fromK("download_count")),
			},
			Deps: fromV("dependencies"), Decoder: "gem-dependencies",
		},
		Promotion: storage.Promotion{Licenses: licenseList("sv.metadata->'licenses'")},
	}
}
