// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	formatswift "github.com/openartifact/openartifact/internal/format/swift"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
)

// swiftFormat is the registration of the swift format.
func swiftFormat() plugin.Format {
	return plain("swift", baseURLHosted(formatswift.NewHTTPHandler),
		plugin.NoAnalysis,
		clientsetup.SwiftBuilder, proxy.SwiftHooks, &group.SwiftHooks, swiftStorage())
}

// swiftStorage describes how the generic engines treat swift rows: one row per
// release, its description and license URL from the publisher's metadata.
func swiftStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindSwift)},
		Detail: storage.Detail{
			Description: fromV("metadata.description"), License: fromV("metadata.licenseURL"),
			Info: []storage.InfoField{entry("scope", fromP("scope"))},
		},
	}
}
