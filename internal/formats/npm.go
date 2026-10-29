// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/vuln"
)

// npmFormat is the registration of the npm format.
func npmFormat() plugin.Format {
	return plugin.Definition{
		ID: "npm",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			h := npmHosted{formatnpm.NewHTTPHandler(d.Log, d.DB, d.Served, d.BaseURL)}

			publish, ok := plugin.REST[npmPublisher](d, "npm")
			if !ok {
				return h, nil
			}

			return npmPublishing{npmHosted: h, publish: publish}, nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.NPMRule,
			Deps:     &pkgdeps.NPMHooks,
			License:  &licensemeta.NPMHooks,
			Vuln:     &vuln.NPMHooks,
		},
		Snippets: clientsetup.NPMBuilder,
		Mirror:   plugin.ProxyOf(proxy.NPMHooks),
		Merge:    plugin.GroupOf(&group.NPMHooks),
		Meta:     npmLifecycle(),
		Store:    npmStorage(),
	}
}

// npmTouchPackument bumps the packument validator of the package of file $1.
const npmTouchPackument = `UPDATE packages SET updated_at = now()
	 WHERE id = (SELECT v.package_id FROM package_files f JOIN package_versions v ON v.id = f.version_id WHERE f.id = $1::uuid)`

// npmStorage describes how the generic engines treat npm rows: the dist-tags are
// package_tags, the packument is regenerated from the rows, and a version a
// dist-tag points at is never removed by retention.
func npmStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:      string(models.TrashKindNPM),
			RegenKeys: "p.id::text, ''",
			OnTrash:   []string{npmTouchPackument},
			OnRestore: []string{npmTouchPackument},
			// The tags of a purged version go with it, unless another live row of
			// the same version string stays.
			OnPurge: []string{
				`DELETE FROM package_tags t USING package_versions v, package_files f
	 WHERE f.id = $1::uuid AND v.id = f.version_id AND t.package_id = v.package_id AND t.version = v.version
	   AND NOT EXISTS (SELECT 1 FROM package_versions o
	                    WHERE o.package_id = v.package_id AND o.version = v.version AND o.id <> v.id AND o.deleted_at IS NULL)`,
				npmTouchPackument,
			},
		},
		Cleanup: storage.Cleanup{
			ProtectExpr: "EXISTS (SELECT 1 FROM package_tags t WHERE t.package_id = v.package_id AND t.version = v.version)",
		},
		Federation: &storage.Federation{
			Unit: storage.UnitVersion, Tags: true, TouchPackage: true, StaleAttr: "packument_stale",
			LocalPackageAttrs: []string{"latest_version", "packument_stale"},
		},
		Detail: storage.Detail{
			Description: fromV("manifest_json.description"), License: fromV("manifest_json.license"),
			Homepage: fromV("manifest_json.homepage"), SHA1: fromC("shasum"),
			Info: []storage.InfoField{entry("integrity", fromC("integrity")), entry("scope", fromP("scope"))},
			Deps: fromV("manifest_json"), Decoder: "npm-manifest",
		},
		Promotion: storage.Promotion{
			Tags:  true,
			Regen: storage.RegenNPM,
			// The latest_version and the stale flag are worker output of the
			// destination, not state to copy.
			PackageAttrs: "sp.attrs - 'latest_version' - 'packument_stale'",
			AfterCopy: []string{
				// A package left without a latest tag gets one on the copied
				// version, as a first publish does.
				`INSERT INTO package_tags (package_id, tag, version)
	SELECT $2::uuid, 'latest', v.version FROM package_versions v WHERE v.id = $3::uuid AND $1::uuid IS NOT NULL
	ON CONFLICT (package_id, tag) DO NOTHING`,
				`UPDATE packages SET updated_at = now(),
	       attrs = attrs || jsonb_build_object('packument_stale', true,
	                        'latest_version', COALESCE((SELECT t.version FROM package_tags t WHERE t.package_id = $2::uuid AND t.tag = 'latest'), ''))
	 WHERE id = $2::uuid AND $1::uuid IS NOT NULL AND $3::uuid IS NOT NULL`,
			},
		},
		Backup: storage.Backup{Regen: storage.RegenNPM},
	}
}
