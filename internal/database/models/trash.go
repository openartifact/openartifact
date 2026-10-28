// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// TrashKind names a kind of soft-deletable row. The value is the stable
// identifier used in the trash API (/api/v1/trash/{kind}/{id}) and in audit
// details; the table behind each kind lives in internal/database/trash_kinds.go.
type TrashKind string

const (
	// TrashKindRepository is a whole repository (repositories.deleted_at).
	TrashKindRepository TrashKind = "repository"

	TrashKindMaven             TrashKind = "maven_artifact"
	TrashKindIvy               TrashKind = "ivy_artifact"
	TrashKindSBT               TrashKind = "sbt_artifact"
	TrashKindNPM               TrashKind = "npm_version"
	TrashKindPyPI              TrashKind = "pypi_package"
	TrashKindNuGet             TrashKind = "nuget_package"
	TrashKindGoMod             TrashKind = "go_module"
	TrashKindCargo             TrashKind = "cargo_crate"
	TrashKindComposer          TrashKind = "composer"
	TrashKindRubyGems          TrashKind = "rubygems_gem"
	TrashKindConda             TrashKind = "conda_package"
	TrashKindPacman            TrashKind = "pacman"
	TrashKindAnsible           TrashKind = "ansible"
	TrashKindConan             TrashKind = "conan_package"
	TrashKindTerraformModule   TrashKind = "terraform_module"
	TrashKindTerraformProvider TrashKind = "terraform_provider"
	TrashKindNix               TrashKind = "nix_path"
	TrashKindApk               TrashKind = "apk_package"
	TrashKindHelm              TrashKind = "helm_chart"
	TrashKindRaw               TrashKind = "raw_artifact"
	TrashKindDockerManifest    TrashKind = "docker_manifest"
	TrashKindDockerTag         TrashKind = "docker_tag"
	TrashKindRPM               TrashKind = "rpm_package"
	TrashKindDeb               TrashKind = "deb_package"
)

// TrashItem is one entry of the trash listing: a soft-deleted repository or
// package row.
type TrashItem struct {
	// ID is the primary key of the trashed row (the repository id for
	// TrashKindRepository). Together with Kind it addresses the item.
	ID   string
	Kind TrashKind
	// RepositoryID/RepositoryName identify the owning repository (the trashed
	// repository itself for TrashKindRepository).
	RepositoryID   string
	RepositoryName string
	Format         string
	// Name is the human-readable identity of the item: package name, file
	// path, image:tag, or the repository name.
	Name string
	// Version is empty for kinds without a version (raw files, repositories).
	Version   string
	SizeBytes int64
	DeletedAt time.Time
	DeletedBy string
	// PurgeAt is when the purge worker removes the item
	// (DeletedAt + the repository's effective retention).
	PurgeAt time.Time
}

// TrashFilter selects trash listing rows.
type TrashFilter struct {
	// RepositoryID restricts the listing to one repository (its trashed items;
	// the repository itself is never listed in a per-repository view).
	RepositoryID string
	// Kind, when non-empty, restricts the listing to one kind.
	Kind TrashKind
	// IncludeRepositories adds trashed repositories to the listing (global,
	// admin-only view).
	IncludeRepositories bool
	Page                int
	Limit               int
}

// TrashRowParams describes a request to soft-delete one package row.
type TrashRowParams struct {
	RepositoryID string
	Kind         TrashKind
	// ID is the primary key of the row in the kind's table.
	ID string
	// DeletedBy is recorded in deleted_by (username).
	DeletedBy string
}

// TrashPurgeResult describes a purged item so the caller can free blobs,
// release quota and report the event.
type TrashPurgeResult struct {
	Kind           TrashKind
	ID             string
	RepositoryID   string
	RepositoryName string
	Format         string
	Name           string
	Version        string
	SizeBytes      int64
	// BlobSHA256 lists every blob the purged rows referenced; the caller frees
	// them with storage.SafeDeleteBlob (which keeps blobs still referenced
	// elsewhere).
	BlobSHA256 []string
	// MetadataBlobSHA256 lists the generated-metadata blobs (repodata, dists,
	// maven-metadata.xml) of a purged repository. They have no blobs row, so
	// the caller frees them with storage.ReclaimMetadataBlob.
	MetadataBlobSHA256 []string
	// QuotaArtifacts is the number of quota-counted package rows removed
	// (0 for formats that do not enforce quotas); QuotaBytes their size.
	QuotaArtifacts int
	QuotaBytes     int64
}

// TrashRef identifies one expired item for the purge worker.
type TrashRef struct {
	Kind TrashKind
	ID   string
}
