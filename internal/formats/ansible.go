// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatansible "github.com/openartifact/openartifact/internal/format/ansible"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
)

// ansibleFormat is the registration of the ansible format.
func ansibleFormat() plugin.Format {
	return plugin.Definition{
		ID: "ansible",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return formatansible.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL), nil
		},
		Supply:   plugin.NoAnalysis,
		Snippets: clientsetup.AnsibleBuilder,
		Mirror:   plugin.ProxyOf(proxy.AnsibleHooks),
		Merge:    plugin.GroupOf(&group.AnsibleHooks),
		Meta:     lifecycle.None,
		Store:    ansibleStorage(),
	}
}

// ansibleStorage describes how the generic engines treat ansible rows.
func ansibleStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{Kind: string(models.TrashKindAnsible)},
		Detail: storage.Detail{
			Info: []storage.InfoField{entry("namespace", fromP("namespace")), entry("requires_ansible", fromV("requires_ansible"))},
			Deps: fromV("dependencies"), Decoder: "ansible-map",
		},
	}
}
