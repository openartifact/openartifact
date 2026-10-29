// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatconan "github.com/openartifact/openartifact/internal/format/conan"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
)

// conanFormat is the registration of the conan format.
func conanFormat() plugin.Format {
	return plugin.Definition{
		ID: "conan",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatconan.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.ConanRule,
			Deps:     &pkgdeps.ConanHooks,
		},
		Snippets: clientsetup.ConanBuilder,
		Mirror:   plugin.ProxyOf(proxy.ConanHooks),
		Merge:    plugin.GroupOf(&group.ConanHooks),
		Meta:     lifecycle.None,
		Store:    conanStorage(),
	}
}

// conanStorage describes how the generic engines treat conan rows: a recipe
// revision is promoted whole, with its binary packages, and the revision is not
// part of the cleanup version.
func conanStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:        string(models.TrashKindConan),
			VersionExpr: "v.version || '@' || (v.metadata->>'user_channel')",
		},
		Cleanup: storage.Cleanup{VersionExpr: "split_part(v.version_key, '#', 1)"},
		Detail: storage.Detail{
			MD5: fromC("md5"), SHA1: fromC("sha1"),
			Info: []storage.InfoField{entry("user_channel", fromV("user_channel")), entry("recipe_revision", fromV("recipe_revision"))},
		},
		Promotion:  storage.Promotion{WholeVersion: true, SharedVersion: true},
		SideTables: []storage.SideTable{{Name: "conan_files"}},
	}
}
