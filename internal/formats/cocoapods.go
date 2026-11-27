// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/proxy"
)

// cocoapodsFormat is the registration of the cocoapods format: proxy
// repositories of a CocoaPods CDN, which keep their files in the proxy cache and
// have no package rows (so no storage descriptor, and nothing for the recovery
// journal beyond the cache entries). There are no group repositories: a group
// would have to merge the shards of its members and relink their podspecs, which
// the first-hit resolution of the groups does not do.
func cocoapodsFormat() plugin.Format {
	return plugin.Definition{
		ID:        "cocoapods",
		NewHosted: func(*plugin.Deps) (plugin.Hosted, error) { return noHosted{}, nil },
		Supply:    plugin.NoAnalysis,
		Snippets:  clientsetup.CocoaPodsBuilder,
		Mirror:    plugin.ProxyOf(proxy.CocoaPodsHooks),
		Merge:     plugin.NoGroup,
		Meta:      lifecycle.None,
		Store:     plugin.NoStorage,
	}
}
