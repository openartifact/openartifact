// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatterraform "github.com/openartifact/openartifact/internal/format/terraform"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
)

// terraformFormat is the registration of the terraform format.
func terraformFormat() plugin.Format {
	return plugin.Definition{
		ID: "terraform",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := formatterraform.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL).WithTicketKey(d.HMACKey)
			// A nil resolver or proxy service is left out: a nil pointer inside the
			// interface is not a nil interface. Without them the provider download
			// endpoint answers 503, as for an unsigned repository.
			if d.GPG != nil {
				h.WithSigner(d.GPG)
			}

			// The tickets of proxy repositories are minted by the proxy service and
			// redeemed through the hosted handler's credential-free route.
			if d.Proxy != nil {
				d.Proxy.WithTerraformTicketKey(d.HMACKey)
				h.WithProxy(d.Proxy)
			}

			return terraformHosted{h}, nil
		},
		Supply:   plugin.NoAnalysis,
		Snippets: clientsetup.TerraformBuilder,
		Mirror:   plugin.ProxyOf(proxy.TerraformHooks),
		Merge:    plugin.GroupOf(&group.TerraformHooks),
		Meta:     lifecycle.None,
	}
}
