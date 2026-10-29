// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatapk "github.com/openartifact/openartifact/internal/format/apk"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
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
		Store:    apkStorage(),
	}
}

// apkStorage describes how the generic engines treat apk rows.
func apkStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:     string(models.TrashKindApk),
			NameExpr: "p.name || ' (' || f.arch || ')'",
		},
		Cleanup: storage.Cleanup{VersionExpr: keyHead},
		Detail: storage.Detail{
			Description: fromV("description"), License: fromV("license"), Homepage: fromV("url"),
			Info: []storage.InfoField{
				entry("branch", fromP("branch")), entry("repo", fromP("repo")), entry("origin", fromV("origin")),
				entry("maintainer", fromV("maintainer")), entry("commit", fromV("commit")), entry("checksum", fromV("checksum")),
				entry("data_sha256", fromV("data_sha256")), entry("installed_size", fromF("installed_size")),
				entry("build_time", fromF("build_time")), entry("download_count", fromK("download_count")),
			},
		},
		Promotion: storage.Promotion{Regen: storage.RegenApk, Licenses: licenseList("sv.metadata->'license'")},
		Backup:    storage.Backup{Regen: storage.RegenApk},
	}
}
