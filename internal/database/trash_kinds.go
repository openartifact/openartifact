// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// trashSpec describes one soft-deletable row kind: the table that holds it and
// the SQL fragments the generic trash operations (soft delete, listing,
// restore, purge) need. Every fragment is a compile-time constant of this file;
// nothing user-controlled is ever interpolated into the generated SQL.
//
// All fragments are written against the table alias "t".
type trashSpec struct {
	kind   models.TrashKind
	table  string
	format string
	// repoID is a scalar expression yielding the owning repository id.
	repoID string
	// name and version are text expressions for the trash listing.
	name    string
	version string
	// blobCols are the uuid columns referencing blobs(id).
	blobCols []string
	// quota marks formats whose uploads reserve storage_quotas usage per row
	// (deb, rpm, maven, helm, npm, rubygems); a purge gives it back.
	quota bool
	// listWhere is an extra predicate applied to the trash listing (to hide rows
	// that are listed under another item), "" for none.
	listWhere string
	// cascade statements run in the soft-delete transaction after the row is
	// stamped. $1 is the row id, $2 the actor. They share the row's deleted_at.
	cascade []string
}

// blobSizeExpr is the total size of the blobs the row references.
func (s *trashSpec) blobSizeExpr() string {
	if len(s.blobCols) == 0 {
		return "0::bigint"
	}

	return "(SELECT COALESCE(SUM(bb.size_bytes), 0)::bigint FROM blobs bb WHERE bb.id IN (" + strings.Join(s.blobCols, ", ") + "))"
}

// blobSHAExpr aggregates the sha256 of the referenced blobs, comma separated.
func (s *trashSpec) blobSHAExpr() string {
	if len(s.blobCols) == 0 {
		return "''::text"
	}

	return "COALESCE((SELECT string_agg(DISTINCT bb.sha256, ',') FROM blobs bb WHERE bb.id IN (" + strings.Join(s.blobCols, ", ") + ")), '')"
}

// trashSpecs lists every soft-deletable package kind, in the order the trash
// listing and the purge worker visit them. Adding a package table with a
// deleted_at column means adding it here; TestTrashSpecsCoverEveryDeletedAtTable
// fails otherwise.
//
//nolint:gochecknoglobals // immutable registry
var trashSpecs = []*trashSpec{
	{
		kind: models.TrashKindMaven, table: "maven_artifacts", format: "maven", quota: true,
		repoID: "t.repository_id", name: "t.repo_path", version: "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindIvy, table: "ivy_artifacts", format: "ivy",
		repoID:   "t.repository_id",
		name:     "t.organisation || '/' || t.module || '/' || t.artifact_name || '.' || t.artifact_ext",
		version:  "t.revision",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindSBT, table: "sbt_artifacts", format: "sbt",
		repoID:   "t.repository_id",
		name:     "t.group_id || ':' || t.artifact_id || '/' || t.filename",
		version:  "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindNPM, table: "npm_versions", format: "npm", quota: true,
		repoID:   "(SELECT np.repository_id FROM npm_packages np WHERE np.id = t.package_id)",
		name:     "(SELECT np.full_name FROM npm_packages np WHERE np.id = t.package_id)",
		version:  "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindPyPI, table: "pypi_packages", format: "pypi",
		repoID: "t.repository_id", name: "t.filename", version: "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindNuGet, table: "nuget_packages", format: "nuget",
		repoID: "t.repository_id", name: "t.package_id", version: "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindGoMod, table: "gomod_modules", format: "go",
		repoID: "t.repository_id", name: "t.module_path", version: "t.version",
		blobCols: []string{"t.zip_blob_id", "t.mod_blob_id"},
	},
	{
		kind: models.TrashKindCargo, table: "cargo_crates", format: "cargo",
		repoID: "t.repository_id", name: "t.name", version: "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindRubyGems, table: "rubygems_gems", format: "rubygems", quota: true,
		repoID:   "t.repository_id",
		name:     "t.name || CASE WHEN t.platform IN ('', 'ruby') THEN '' ELSE ' (' || t.platform || ')' END",
		version:  "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindConda, table: "conda_packages", format: "conda",
		repoID:   "t.repository_id",
		name:     "t.platform || '/' || t.name",
		version:  "t.version || '-' || t.build_string",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindConan, table: "conan_packages", format: "conan",
		repoID:   "t.repository_id",
		name:     "t.name",
		version:  "t.version || '@' || t.user_channel",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindHelm, table: "helm_charts", format: "helm", quota: true,
		repoID: "t.repository_id", name: "t.name", version: "t.version",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindRaw, table: "raw_artifacts", format: "raw",
		repoID: "t.repository_id", name: "t.path", version: "''",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindDockerManifest, table: "docker_manifests", format: "docker",
		repoID: "t.repository_id", name: "t.image", version: "t.digest",
		blobCols: []string{"t.blob_id"},
		// Deleting a manifest by digest also removes the tags that pointed at it.
		// They are trashed with the manifest (same deleted_at) and restored with it.
		cascade: []string{`UPDATE docker_tags dt SET deleted_at = m.deleted_at, deleted_by = $2
			FROM docker_manifests m
			WHERE m.id = $1 AND dt.repository_id = m.repository_id AND dt.image = m.image
			  AND dt.manifest_digest = m.digest AND dt.deleted_at IS NULL`},
	},
	{
		kind: models.TrashKindDockerTag, table: "docker_tags", format: "docker",
		repoID: "t.repository_id", name: "t.image", version: "t.name",
		// A tag trashed together with its manifest is listed under the manifest.
		listWhere: `NOT EXISTS (SELECT 1 FROM docker_manifests m
			WHERE m.repository_id = t.repository_id AND m.image = t.image
			  AND m.digest = t.manifest_digest AND m.deleted_at = t.deleted_at)`,
	},
	{
		kind: models.TrashKindRPM, table: "rpm_packages", format: "rpm", quota: true,
		repoID: "t.repository_id", name: "t.name", version: "t.version || '-' || t.release || '.' || t.arch",
		blobCols: []string{"t.blob_id"},
	},
	{
		kind: models.TrashKindDeb, table: "deb_packages", format: "deb", quota: true,
		repoID: "t.repository_id", name: "t.name", version: "t.version || '_' || t.arch",
		blobCols: []string{"t.blob_id"},
	},
}

// trashSpecByKind returns the spec registered for kind.
func trashSpecByKind(kind models.TrashKind) (*trashSpec, bool) {
	for _, s := range trashSpecs {
		if s.kind == kind {
			return s, true
		}
	}

	return nil, false
}
