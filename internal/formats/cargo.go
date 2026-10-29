// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatcargo "github.com/openartifact/openartifact/internal/format/cargo"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// cargoFormat is the registration of the cargo format.
func cargoFormat() plugin.Format {
	return plain("cargo", baseURLHosted(formatcargo.NewHTTPHandler),
		plugin.Analysis{
			Manifest: &manifest.CargoRule,
			Deps:     &pkgdeps.CargoHooks,
			License:  &licensemeta.CargoHooks,
			Vuln:     &vuln.CargoHooks,
		},
		clientsetup.CargoBuilder, proxy.CargoHooks, &group.CargoHooks, cargoStorage())
}

// cargoStorage describes how the generic engines treat cargo rows.
func cargoStorage() storage.Storage {
	return storage.Storage{
		Trash:      storage.Trash{Kind: string(models.TrashKindCargo)},
		Federation: &storage.Federation{QuarantineKeys: cargoQuarantineKeys},
		Detail: storage.Detail{
			Description: fromV("description"), License: fromV("license"), Homepage: fromV("homepage"),
			Info: []storage.InfoField{
				entry("documentation", fromV("documentation")), entry("repository", fromV("repository")),
				entry("keywords", fromV("keywords")), entry("categories", fromV("categories")), entry("authors", fromV("authors")),
				entry("yanked", fromV("yanked")), entry("download_count", fromK("download_count")),
			},
			Deps: fromV("metadata_json"), Decoder: "cargo-metadata",
		},
		Promotion: storage.Promotion{Licenses: licenseList("sv.metadata->'license'")},
	}
}
