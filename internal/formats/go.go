// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// goFormat is the registration of the go format.
func goFormat() plugin.Format {
	return plugin.Definition{
		ID: "go",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := formatgomod.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL)
			serve := http.HandlerFunc(h.HandlePut)

			if d.CountUploads != nil {
				serve = d.CountUploads("go", serve)
			}

			return goHosted{HTTPHandler: h, serve: serve}, nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.GoRule,
			Deps:     &pkgdeps.GoHooks,
			Vuln:     &vuln.GoHooks,
		},
		Snippets: clientsetup.GoBuilder,
		Mirror:   plugin.ProxyOf(proxy.GoHooks),
		Merge:    plugin.GroupOf(&group.GoHooks),
		Meta:     lifecycle.None,
	}
}
