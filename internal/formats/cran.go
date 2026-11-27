// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatcran "github.com/openartifact/openartifact/internal/format/cran"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
)

// cranFormat is the registration of the cran format.
func cranFormat() plugin.Format {
	return plain("cran", baseURLHosted(formatcran.NewHTTPHandler), plugin.NoAnalysis,
		clientsetup.CRANBuilder, proxy.CRANHooks, &group.CRANHooks, cranStorage())
}

// cranStorage describes how the generic engines treat cran rows: one row per
// package file (the source tarball and each binary of a version), shown by its
// file name. The binaries of a version join its source, so a version holds
// independent files.
func cranStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindCRAN), NameExpr: "f.filename"},
		Detail: storage.Detail{
			Summary: fromV("title"), Description: fromV("description"), License: fromV("license"), Homepage: fromV("url"),
			MD5: fromC("md5"),
			Info: []storage.InfoField{
				entry("maintainer", fromV("maintainer")), entry("tree", fromK("arch")),
				entry("depends", fromF("Depends")), entry("imports", fromF("Imports")), entry("linking_to", fromF("LinkingTo")),
				entry("needs_compilation", fromF("NeedsCompilation")), entry("built", fromF("Built")),
			},
		},
		Promotion: storage.Promotion{SharedVersion: true, Licenses: licenseList("sv.metadata->'license'")},
	}
}
