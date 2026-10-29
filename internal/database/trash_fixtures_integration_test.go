// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"testing"

	"github.com/openartifact/openartifact/internal/format/storage"
)

// registerTrashFixtures registers the storage descriptors the trash, cleanup and
// blob-reference tests rely on, with the values the format plugins register
// (the mapping the plugin layer must reproduce is documented on
// storage.Trash/storage.Cleanup), and resets the registry when the test ends.
func registerTrashFixtures(tb testing.TB) {
	tb.Helper()

	storage.Reset()
	tb.Cleanup(storage.Reset)

	storage.Register("maven", storage.Storage{
		Trash: storage.Trash{
			Kind: "maven_artifact", NameExpr: "f.path",
			RegenKeys: "p.attrs->>'group_id', p.attrs->>'artifact_id'",
		},
	})
	storage.Register("npm", storage.Storage{
		Trash: storage.Trash{
			Kind:      "npm_version",
			RegenKeys: "p.id::text, ''::text",
			OnTrash:   []string{npmTouchPackument},
			OnRestore: []string{npmTouchPackument},
			OnPurge: []string{
				`DELETE FROM package_tags t USING package_files f, package_versions v
				  WHERE f.id = $1::uuid AND v.id = f.version_id AND t.package_id = v.package_id AND t.version = v.version
				    AND NOT EXISTS (SELECT 1 FROM package_versions o
				                     WHERE o.package_id = v.package_id AND o.version = v.version AND o.id <> v.id AND o.deleted_at IS NULL)`,
				npmTouchPackument,
			},
		},
	})
	storage.Register("go", storage.Storage{Trash: storage.Trash{Kind: "go_module", Scope: storage.ScopeVersion}})
	storage.Register("helm", storage.Storage{Trash: storage.Trash{Kind: "helm_chart"}})
	storage.Register("rubygems", storage.Storage{Trash: storage.Trash{Kind: "rubygems_gem"}})
	storage.Register("raw", storage.Storage{
		Trash: storage.Trash{Kind: "raw_artifact", NameExpr: "f.path", VersionExpr: "''"},
	})
	storage.Register("rpm", storage.Storage{
		Trash: storage.Trash{Kind: "rpm_package", VersionExpr: "v.version || '-' || (f.attrs->>'release') || '.' || f.arch"},
	})
	storage.Register("deb", storage.Storage{
		Trash: storage.Trash{Kind: "deb_package", VersionExpr: "v.version || '_' || f.arch"},
	})
}

// npmTouchPackument bumps packages.updated_at, the validator of the packument.
const npmTouchPackument = `UPDATE packages SET updated_at = now()
	WHERE id = (SELECT v.package_id FROM package_files f JOIN package_versions v ON v.id = f.version_id WHERE f.id = $1::uuid)`
