// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	formatpacman "github.com/openartifact/openartifact/internal/format/pacman"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
)

// pacmanFormat is the registration of the pacman format.
func pacmanFormat() plugin.Format {
	return plugin.Definition{
		ID: "pacman",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := formatpacman.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL)
			if regen, ok := lifecycle.Worker[formatpacman.Regenerator](d.Workers, "pacman"); ok {
				h.SetRegenerator(regen)
			}

			return h, nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.PacmanRule,
			Deps:     &pkgdeps.PacmanHooks,
			SBOM:     &sbom.PacmanHooks,
		},
		Snippets: clientsetup.PacmanBuilder,
		Mirror:   plugin.ProxyOf(proxy.PacmanHooks),
		Merge:    plugin.GroupOf(&group.PacmanHooks),
		Meta:     repoLifecycle("pacman", false),
		Store:    pacmanStorage(),
	}
}

// pacmanStorage describes how the generic engines treat pacman rows: the file
// list of the side table is derived from the blob, so the journal skips it.
func pacmanStorage() storage.Storage {
	return storage.Storage{
		Trash:   storage.Trash{Kind: string(models.TrashKindPacman)},
		Cleanup: storage.Cleanup{VersionExpr: keyHead},
		Detail: storage.Detail{
			MD5: fromF("md5"),
			Info: []storage.InfoField{
				entry("base", fromV("base")), entry("repo_arch", fromV("repo_arch")), entry("description", fromV("description")),
				entry("url", fromV("url")), entry("packager", fromV("packager")), entry("build_date", fromF("build_time")),
				entry("installed_size", fromF("installed_size")), entry("signature_source", fromS("signature_source")),
			},
			Deps: fromV("depends"), Decoder: "pacman-depends",
		},
		Promotion:  storage.Promotion{Regen: storage.RegenPacman, Licenses: licenseList("sv.metadata->'licenses'")},
		Backup:     storage.Backup{Regen: storage.RegenPacman},
		SideTables: []storage.SideTable{{Name: "pacman_files", Exclude: []string{"file_list"}}},
	}
}
