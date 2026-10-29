// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatcomposer "github.com/openartifact/openartifact/internal/format/composer"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// composerFormat is the registration of the composer format.
func composerFormat() plugin.Format {
	return plugin.Definition{
		ID: "composer",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatcomposer.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL).WithMetrics(d.Metrics), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.ComposerRule,
			Deps:     &pkgdeps.ComposerHooks,
			License:  &licensemeta.ComposerHooks,
			Vuln:     &vuln.ComposerHooks,
		},
		Snippets: clientsetup.ComposerBuilder,
		Mirror:   plugin.ProxyOf(proxy.ComposerHooks),
		Merge:    plugin.GroupOf(&group.ComposerHooks),
		Meta:     lifecycle.None,
		Store:    composerStorage(),
	}
}

// composerStorage describes how the generic engines treat composer rows.
func composerStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindComposer)},
		Detail: storage.Detail{
			Description: fromV("composer_json.description"), Homepage: fromV("composer_json.homepage"), SHA1: fromF("sha1"),
			Info: []storage.InfoField{entry("version_normalized", fromK("version_key"))},
			Deps: fromV("composer_json.require"), Decoder: "composer-require",
		},
		Promotion: storage.Promotion{Licenses: licenseList("sv.metadata->'composer_json'->'license'")},
	}
}
