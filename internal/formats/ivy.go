// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formativy "github.com/openartifact/openartifact/internal/format/ivy"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
)

// ivyFormat is the registration of the ivy format.
func ivyFormat() plugin.Format {
	return plugin.Definition{
		ID: "ivy",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formativy.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.IvyRule,
			Deps:     &pkgdeps.IvyHooks,
			License:  &licensemeta.IvyHooks,
			SBOM:     &sbom.MavenHooks,
		},
		Snippets: clientsetup.IvyBuilder,
		Mirror:   plugin.ProxyOf(proxy.IvyHooks),
		Merge:    plugin.GroupOf(&group.IvyHooks),
		Meta:     lifecycle.None,
		Store:    ivyStorage(),
	}
}

// ivyStorage describes how the generic engines treat ivy rows.
func ivyStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:     string(models.TrashKindIvy),
			NameExpr: "p.attrs->>'organisation' || '/' || p.attrs->>'module' || '/' || f.filename",
		},
		Detail: storage.Detail{Info: []storage.InfoField{
			entry("organisation", fromP("organisation")), entry("module", fromP("module")),
			entry("artifact_type", fromF("artifact_type")), entry("artifact_ext", fromF("artifact_ext")),
		}},
		Promotion: storage.Promotion{SharedVersion: true},
	}
}
