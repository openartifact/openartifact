// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/webhook"
)

// noHosted is the hosted handler of a format that has no hosted repositories
// (repotype.Spec.NoHosted): it registers no routes, since the proxy and group
// dispatchers answer every request of such a repository.
type noHosted struct{}

func (noHosted) RegisterRepoRoutes(chi.Router)  {}
func (noHosted) SetPublisher(webhook.Publisher) {}

// huggingFaceFormat is the registration of the huggingface format: proxy and
// group repositories of the Hugging Face Hub, which keep their files in the
// proxy cache and have no package rows (so no storage descriptor, and nothing
// for the recovery journal beyond the cache entries).
func huggingFaceFormat() plugin.Format {
	return plugin.Definition{
		ID:        "huggingface",
		NewHosted: func(*plugin.Deps) (plugin.Hosted, error) { return noHosted{}, nil },
		Supply:    plugin.NoAnalysis,
		Snippets:  clientsetup.HuggingFaceBuilder,
		Mirror:    plugin.ProxyOf(proxy.HuggingFaceHooks),
		Merge:     plugin.FirstHitGroup,
		Meta:      lifecycle.None,
		Store:     plugin.NoStorage,
	}
}
