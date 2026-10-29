// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
)

// dockerFormat is the registration of the docker format.
func dockerFormat() plugin.Format {
	return plugin.Definition{
		ID: "docker",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := formatdocker.NewHTTPHandler(d.Log, d.DB, d.Served)
			h.SetUploadDir(d.DockerUploadDir)

			if d.DockerAuthorizer != nil {
				h.SetAuthorizer(d.DockerAuthorizer)
			}

			return dockerHosted{h}, nil
		},
		Supply: plugin.Analysis{
			SBOM: &sbom.DockerHooks,
		},
		Snippets: clientsetup.DockerBuilder,
		Mirror:   plugin.ProxyOf(proxy.DockerHooks),
		Merge:    plugin.FirstHitGroup,
		Meta:     dockerLifecycle(),
		// Docker keeps dedicated tables, which the generic engines handle explicitly.
		Store: plugin.NoStorage,
	}
}
