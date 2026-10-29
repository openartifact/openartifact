// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formathelm "github.com/openartifact/openartifact/internal/format/helm"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
)

// helmFormat is the registration of the helm format.
func helmFormat() plugin.Format {
	return plain("helm", baseURLHosted(formathelm.NewHTTPHandler),
		plugin.Analysis{
			Manifest: &manifest.HelmRule,
			Deps:     &pkgdeps.HelmHooks,
		},
		clientsetup.HelmBuilder, proxy.HelmHooks, &group.HelmHooks, helmStorage())
}

// helmStorage describes how the generic engines treat helm rows.
func helmStorage() storage.Storage {
	return storage.Storage{
		Trash:      storage.Trash{Kind: string(models.TrashKindHelm)},
		Federation: &storage.Federation{QuarantineKeys: helmQuarantineKeys},
		Detail: storage.Detail{
			Description: fromV("description"), Homepage: fromV("home"),
			Info: []storage.InfoField{
				entry("app_version", fromV("app_version")), entry("api_version", fromV("api_version")), entry("type", fromV("type")),
				entry("keywords", fromV("keywords")), entry("icon", fromV("icon")), entry("sources", joined(fromV("sources"), "\n")),
			},
		},
	}
}
