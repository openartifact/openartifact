// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
)

// apkFormat is the registration of the apk format.
func apkFormat() plugin.Format {
	return plugin.Definition{
		ID: "apk",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := formatapk.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL).WithMetrics(d.Metrics)
			if d.APKKeys != nil {
				h.WithKeys(d.APKKeys)
			}

			if regen, ok := lifecycle.Worker[formatapk.Regenerator](d.Workers, "apk"); ok {
				h.WithRegenerator(regen)
			}

			return h, nil
		},
		Supply:   plugin.NoAnalysis,
		Snippets: clientsetup.APKBuilder,
		Mirror:   plugin.ProxyOf(proxy.APKHooks),
		Merge:    plugin.GroupOf(&group.ApkHooks),
		Meta:     repoLifecycle("apk", false),
	}
}
