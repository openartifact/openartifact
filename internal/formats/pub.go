// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatpub "github.com/openartifact/openartifact/internal/format/pub"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// pubFormat is the registration of the pub format (Dart and Flutter packages).
func pubFormat() plugin.Format {
	return plain("pub", baseURLHosted(formatpub.NewHTTPHandler),
		plugin.Analysis{Vuln: &vuln.PubHooks},
		clientsetup.PubBuilder, proxy.PubHooks, &group.PubHooks, pubStorage())
}

// pubStorage describes how the generic engines treat pub rows.
func pubStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindPub)},
		Detail: storage.Detail{
			Description: fromV("pubspec.description"), Homepage: fromV("pubspec.homepage"),
			Info: []storage.InfoField{
				entry("repository", fromV("pubspec.repository")), entry("documentation", fromV("pubspec.documentation")),
				entry("issue_tracker", fromV("pubspec.issue_tracker")), entry("sdk", fromV("pubspec.environment.sdk")),
			},
			Deps: fromV("pubspec"), Decoder: "pubspec",
		},
	}
}
