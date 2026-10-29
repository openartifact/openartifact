// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// mavenFormat is the registration of the maven format.
func mavenFormat() plugin.Format {
	return plugin.Definition{
		ID: "maven",
		NewHosted: func(d *plugin.Deps) (plugin.Hosted, error) {
			return withREST(formatmaven.NewHTTPHandler(d.Log, d.DB, d.Served), d, "maven"), nil
		},
		Supply: plugin.Analysis{
			Manifest: &manifest.MavenRule,
			Deps:     &pkgdeps.MavenHooks,
			License:  &licensemeta.MavenHooks,
			Vuln:     &vuln.MavenHooks,
			SBOM:     &sbom.MavenHooks,
		},
		Snippets: clientsetup.MavenBuilder,
		Mirror:   plugin.ProxyOf(proxy.MavenHooks),
		Merge:    plugin.GroupOf(&group.MavenHooks),
		Meta:     mavenLifecycle(),
		Store:    mavenStorage(),
	}
}

// mavenStorage describes how the generic engines treat maven rows: the jar and
// the pom of a release are one version, a file of a version is a unit of its own,
// and the newest build of a snapshot is never removed by retention.
func mavenStorage() storage.Storage {
	return storage.Storage{
		Trash: storage.Trash{
			Kind:      string(models.TrashKindMaven),
			NameExpr:  "f.path",
			RegenKeys: "p.attrs->>'group_id', p.attrs->>'artifact_id'",
		},
		Cleanup: storage.Cleanup{
			Partition: []string{"p.id", "f.part"},
			ProtectExpr: `COALESCE((v.metadata->>'is_snapshot')::boolean, false)
	 AND DENSE_RANK() OVER (PARTITION BY p.id, v.version
	       ORDER BY v.metadata->>'snapshot_timestamp' DESC NULLS LAST, (v.metadata->>'snapshot_build_number')::int DESC NULLS LAST) = 1`,
		},
		Federation: &storage.Federation{Unit: storage.UnitFile},
		Detail: storage.Detail{
			MD5: fromC("md5"), SHA1: fromC("sha1"),
			Info: []storage.InfoField{
				entry("group_id", fromP("group_id")), entry("artifact_id", fromP("artifact_id")),
				entry("classifier", fromS("classifier")), entry("extension", fromS("extension")),
				entry("snapshot", fromS("is_snapshot")), entry("snapshot_timestamp", fromS("snapshot_timestamp")),
			},
		},
		Promotion:  storage.Promotion{SharedVersion: true},
		Backup:     storage.Backup{Regen: storage.RegenMaven},
		SideTables: []storage.SideTable{{Name: "maven_files"}},
	}
}
