// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatnuget "github.com/openartifact/openartifact/internal/format/nuget"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// nugetFormat is the registration of the nuget format.
func nugetFormat() plugin.Format {
	return plain("nuget", baseURLHosted(formatnuget.NewHTTPHandler),
		plugin.Analysis{
			Manifest: &manifest.NuGetRule,
			Deps:     &pkgdeps.NuGetHooks,
			License:  &licensemeta.NuGetHooks,
			Vuln:     &vuln.NuGetHooks,
		},
		clientsetup.NuGetBuilder, proxy.NuGetHooks, &group.NuGetHooks, nugetStorage())
}

// nugetStorage describes how the generic engines treat nuget rows.
func nugetStorage() storage.Storage {
	return storage.Storage{
		Trash:      storage.Trash{Kind: string(models.TrashKindNuGet)},
		Federation: &storage.Federation{QuarantineKeys: nugetQuarantineKeys},
		Detail: storage.Detail{
			Summary: fromV("summary"), Description: fromV("description"), License: fromV("license_url"), Homepage: fromV("project_url"),
			Info: []storage.InfoField{
				entry("title", fromV("title")), entry("authors", fromV("authors")), entry("owners", fromV("owners")),
				entry("tags", fromV("tags")), entry("min_client_version", fromV("min_client_version")),
				entry("prerelease", fromV("is_prerelease")), entry("download_count", fromK("download_count")),
			},
			Deps: fromV("dependency_groups"), Decoder: "nuget-groups",
		},
	}
}
