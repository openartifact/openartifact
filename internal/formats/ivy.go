// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"slices"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formativy "github.com/openartifact/openartifact/internal/format/ivy"
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
	return plain("ivy", baseURLHosted(formativy.NewHTTPHandler),
		plugin.Analysis{
			Manifest: &manifest.IvyRule,
			Deps:     &pkgdeps.IvyHooks,
			License:  &licensemeta.IvyHooks,
			SBOM:     &sbom.MavenHooks,
		},
		clientsetup.IvyBuilder, proxy.IvyHooks, &group.IvyHooks, ivyStorage())
}

// ivyStorage describes how the generic engines treat ivy rows.
func ivyStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind: string(models.TrashKindIvy),
			// ->> and || share a precedence level and associate left, so each
			// extraction needs its own parentheses.
			NameExpr: "(p.attrs->>'organisation') || '/' || (p.attrs->>'module') || '/' || f.filename",
		},
		Detail: storage.Detail{Info: slices.Concat(
			infoOf(storage.FromPackage, "organisation", "module"),
			infoOf(storage.FromFile, "artifact_type", "artifact_ext"),
		)},
		Promotion: storage.Promotion{SharedVersion: true},
	}
}
