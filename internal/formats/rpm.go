// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/plugin"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// rpmFormat is the registration of the rpm format.
func rpmFormat() plugin.Format {
	return plugin.Definition{
		ID: "rpm",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return withREST(quiet{formatrpm.NewHTTPHandler(d.Log, d.DB, d.Served)}, d, "rpm"), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.RPMRule,
			Deps:     &pkgdeps.RPMHooks,
			Vuln:     &vuln.RPMHooks,
			SBOM:     &sbom.RPMHooks,
		},
		Snippets: clientsetup.RPMBuilder,
		Mirror:   plugin.ProxyOf(proxy.RPMHooks),
		Merge:    plugin.GroupOf(&group.RPMHooks),
		Meta:     repoLifecycle("rpm", true),
		Store:    rpmStorage(),
	}
}

// rpmStorage describes how the generic engines treat rpm rows: the rpm side table
// carries the header range and the parsed extras the detail view reads the
// dependencies from, so the journal keeps all of it.
func rpmStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:        string(models.TrashKindRPM),
			VersionExpr: "v.version || '-' || (f.attrs->>'release') || '.' || f.arch",
		},
		Cleanup: storage.Cleanup{VersionExpr: keyHead},
		Detail: storage.Detail{
			Summary: fromV("summary"), Description: fromV("description"), License: fromV("license"), Homepage: fromV("url"),
			Info: []storage.InfoField{
				entry("release", fromF("release")), entry("epoch", fromF("epoch")), entry("vendor", fromV("vendor")),
				entry("packager", fromV("packager")), entry("group", fromV("group_name")), entry("source_rpm", fromV("source_rpm")),
				entry("build_host", fromV("build_host")), entry("installed_size", fromF("installed_size")),
			},
			Deps: fromS("rpm_extra"), Decoder: "rpm-extra",
		},
		Promotion: storage.Promotion{
			Regen:    storage.RegenRPM,
			Licenses: licenseList("sv.metadata->'license'"),
			// A download resolves by file name alone.
			Collide: "d.filename = s.filename",
			// The destination's repodata is regenerated: flag it stale, creating
			// the rpm_repos row the metadata worker needs when it has none.
			AfterCopy: []string{`WITH marked AS (
	  UPDATE rpm_repos SET metadata_stale = true WHERE repository_id = $1::uuid RETURNING id
	)
	INSERT INTO rpm_repos (repository_id, base_arch)
	SELECT $1::uuid, 'x86_64' WHERE NOT EXISTS (SELECT 1 FROM marked) AND $2::uuid IS NOT NULL AND $3::uuid IS NOT NULL
	ON CONFLICT (repository_id, base_arch) DO UPDATE SET metadata_stale = true`},
		},
		Backup:     storage.Backup{Regen: storage.RegenRPM},
		SideTables: []storage.SideTable{{Name: "rpm_files"}},
	}
}
