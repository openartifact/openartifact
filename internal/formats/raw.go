// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
)

// rawFormat is the registration of the raw format.
func rawFormat() plugin.Format {
	return plugin.Definition{
		ID: "raw",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return withREST(formatraw.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), d, "raw"), nil
		},
		Supply: plugin.Analysis{
			SBOM: &sbom.RawHooks,
		},
		Snippets: clientsetup.RawBuilder,
		Mirror:   plugin.ProxyOf(proxy.RawHooks),
		Merge:    plugin.FirstHitGroup,
		Meta:     lifecycle.None,
		Store:    rawStorage(),
	}
}

// rawStorage describes how the generic engines treat raw rows: every path is a
// file of its own, ranked by retention among the files of its directory.
func rawStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindRaw), NameExpr: "f.path", VersionExpr: "''"},
		Cleanup: storage.Cleanup{
			Partition:   []string{"regexp_replace(f.path, '[^/]*$', '')"},
			VersionExpr: "f.path",
		},
		Federation: &storage.Federation{Unit: storage.UnitFile},
		Detail:     storage.Detail{Info: []storage.InfoField{entry("content_type", fromF("content_type"))}},
	}
}
