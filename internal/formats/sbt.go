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
	formatsbt "github.com/openartifact/openartifact/internal/format/sbt"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
)

// sbtFormat is the registration of the sbt format.
func sbtFormat() plugin.Format {
	return plugin.Definition{
		ID: "sbt",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatsbt.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.SBTRule,
			Deps:     &pkgdeps.SBTHooks,
			License:  &licensemeta.SBTHooks,
			SBOM:     &sbom.MavenHooks,
		},
		Snippets: clientsetup.SBTBuilder,
		Mirror:   plugin.ProxyOf(proxy.SbtHooks),
		Merge:    plugin.GroupOf(&group.SBTHooks),
		Meta:     lifecycle.None,
		Store:    sbtStorage(),
	}
}

// sbtStorage describes how the generic engines treat sbt rows.
func sbtStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindSBT), NameExpr: "p.name || '/' || f.filename"},
		Detail: storage.Detail{Info: []storage.InfoField{
			entry("group_id", fromP("group_id")), entry("artifact_id", fromP("artifact_id")), entry("scala_version", fromK("arch")),
		}},
		Promotion: storage.Promotion{SharedVersion: true},
	}
}
