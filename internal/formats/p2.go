// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/proxy"
)

// p2Routes is the hosted handler of p2, which has no hosted repositories. The
// format is mounted on a prefix of its own (/repos/p2/{name}), and chi runs the
// middleware of a subrouter, here the repository dispatcher that serves proxy
// repositories, only when it has a route; the catch-all answers 404 for what
// the dispatcher does not take, which no repository of the format is.
type p2Routes struct{ noHosted }

func (p2Routes) RegisterRepoRoutes(r chi.Router) { r.HandleFunc("/*", http.NotFound) }

// p2Format is the registration of the p2 format: proxy repositories of Eclipse
// p2 update sites, which keep their files in the proxy cache. There are no
// hosted repositories (so no package rows, storage descriptor or recovery
// journal entry beyond the cache entries) and no groups: a p2 repository merges
// its members with a composite repository of its own, which the proxy relocates.
func p2Format() plugin.Format {
	return plugin.Definition{
		ID:        "p2",
		NewHosted: func(*plugin.Deps) (plugin.Hosted, error) { return p2Routes{}, nil },
		Supply:    plugin.NoAnalysis,
		Snippets:  clientsetup.P2Builder,
		Mirror:    plugin.ProxyOf(proxy.P2Hooks),
		Merge:     plugin.NoGroup,
		Meta:      lifecycle.None,
		Store:     plugin.NoStorage,
	}
}
