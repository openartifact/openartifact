// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatconda "github.com/openartifact/openartifact/internal/format/conda"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
)

// condaFormat is the registration of the conda format.
func condaFormat() plugin.Format {
	return plain("conda", baseURLHosted(formatconda.NewHTTPHandler),
		plugin.Analysis{
			Manifest: &manifest.CondaRule,
			Deps:     &pkgdeps.CondaHooks,
		},
		clientsetup.CondaBuilder, proxy.CondaHooks, &group.CondaHooks, condaStorage())
}

// condaStorage describes how the generic engines treat conda rows: a version is
// told from its build by the key's '|' suffix.
func condaStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:        string(models.TrashKindConda),
			NameExpr:    "f.arch || '/' || p.name",
			VersionExpr: "v.version || '-' || (v.metadata->>'build_string')",
		},
		Cleanup: storage.Cleanup{VersionExpr: `regexp_replace(v.version_key, '\|[^|]*$', '')`},
		Detail: storage.Detail{
			MD5: fromC("md5"),
			Info: []storage.InfoField{
				entry("build_string", fromV("build_string")), entry("build_number", fromF("build_number")),
				entry("extension", fromF("extension")), entry("timestamp_ms", fromF("timestamp_ms")),
			},
			Deps: fromF("depends"), Decoder: "conda-specs",
		},
	}
}
