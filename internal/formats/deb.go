// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package formats

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/clientsetup"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/pkgdeps"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// debFormat is the registration of the deb format.
func debFormat() plugin.Format {
	def := plain("deb", downloadOnly("deb", formatdeb.NewHTTPHandler),
		plugin.Analysis{
			Manifest: &manifest.DebRule,
			Deps:     &pkgdeps.DebHooks,
			Vuln:     &vuln.DebHooks,
			SBOM:     &sbom.DebHooks,
		},
		clientsetup.DebBuilder, proxy.DebHooks, &group.DebHooks, debStorage())
	def.Meta = repoLifecycle("deb", false)

	return def
}

const (
	// debDistribution is the distribution the copied file is published in at
	// the destination: the one of the same name, which Prepare creates.
	debDistribution = `(SELECT dd.id FROM deb_files x
	    JOIN debian_distributions sd ON sd.id = x.distribution_id
	    JOIN debian_distributions dd ON dd.repository_id = $1::uuid AND dd.name = sd.name
	   WHERE x.id = s.id)`
	// debComponent is the component of the copied file at the destination.
	debComponent = `(SELECT dc.id FROM deb_files x
	    JOIN debian_distributions sd ON sd.id = x.distribution_id
	    JOIN debian_components sc ON sc.id = x.component_id
	    JOIN debian_distributions dd ON dd.repository_id = $1::uuid AND dd.name = sd.name
	    JOIN debian_components dc ON dc.distribution_id = dd.id AND dc.name = sc.name
	   WHERE x.id = s.id)`
)

// debStorage describes how the generic engines treat deb rows: one .deb is one
// file whose version_key is version|arch|component and whose part is the
// distribution, with the distribution graph as companion tables.
func debStorage() storage.Storage {
	return storage.Storage{
		Trash:   storage.Trash{Kind: string(models.TrashKindDeb), VersionExpr: "v.version || '_' || f.arch"},
		Cleanup: storage.Cleanup{VersionExpr: keyHead},
		Detail: storage.Detail{
			Summary: fromV("summary"), Description: fromV("description"), License: fromV("license"), Homepage: fromV("homepage"),
			Info: []storage.InfoField{entry("maintainer", fromV("maintainer")), entry("installed_size", fromF("installed_size"))},
			Deps: fromS("control_fields"), Decoder: "deb-control",
		},
		Promotion: storage.Promotion{
			Regen:    storage.RegenDeb,
			Licenses: licenseList("sv.metadata->'license'"),
			// The pool resolves a download by file name alone.
			Collide: "d.filename = s.filename",
			// The destination gets the distribution, component and architecture
			// of the file, as an upload creates them.
			Prepare: []string{
				`INSERT INTO debian_distributions (repository_id, name, codename, description)
	SELECT $1::uuid, sd.name, sd.codename, sd.description
	  FROM deb_files x JOIN debian_distributions sd ON sd.id = x.distribution_id
	 WHERE x.id = $2::uuid
	ON CONFLICT (repository_id, name) DO NOTHING`,
				`INSERT INTO debian_components (distribution_id, name, description)
	SELECT dd.id, sc.name, sc.description
	  FROM deb_files x
	  JOIN debian_distributions sd ON sd.id = x.distribution_id
	  JOIN debian_components sc ON sc.id = x.component_id
	  JOIN debian_distributions dd ON dd.repository_id = $1::uuid AND dd.name = sd.name
	 WHERE x.id = $2::uuid
	ON CONFLICT (distribution_id, name) DO NOTHING`,
				`INSERT INTO debian_architectures (distribution_id, name)
	SELECT dd.id, f.arch
	  FROM package_files f
	  JOIN deb_files x ON x.id = f.id
	  JOIN debian_distributions sd ON sd.id = x.distribution_id
	  JOIN debian_distributions dd ON dd.repository_id = $1::uuid AND dd.name = sd.name
	 WHERE f.id = $2::uuid AND f.arch <> ''
	ON CONFLICT (distribution_id, name) DO NOTHING`,
			},
			VersionKey: "sv.version || '|' || s.arch || '|' || " + debComponent + "::text",
			Part:       debDistribution + "::text",
			SideSet: map[string]string{"deb_files": "jsonb_build_object('distribution_id', " + debDistribution +
				", 'component_id', " + debComponent + ")"},
		},
		Backup: storage.Backup{
			Regen: storage.RegenDeb,
			Companions: []storage.Companion{
				{Name: "debian_distributions", Scope: "t.repository_id = $1", RepoColumn: "repository_id"},
				{
					Name:  "debian_components",
					Scope: "t.distribution_id IN (SELECT id FROM debian_distributions WHERE repository_id = $1)",
					Refs:  []string{"distribution_id"},
				},
				{
					Name:  "debian_architectures",
					Scope: "t.distribution_id IN (SELECT id FROM debian_distributions WHERE repository_id = $1)",
					Refs:  []string{"distribution_id"},
				},
			},
		},
		// control_fields is what the Packages stanza is rendered from and is not
		// derivable without scanning the blobs, so the journal keeps it.
		SideTables: []storage.SideTable{{Name: "deb_files", Refs: []string{"distribution_id", "component_id"}}},
	}
}
