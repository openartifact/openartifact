// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// formatTable maps a repository format to the rows a cleanup policy ranks and
// removes. Every table has an `id` PK, a `repository_id` (possibly through
// `source`), `created_at`, `deleted_at` and FKs into the global blobs table.
type formatTable struct {
	// table is the table the rows are deleted from (and trashed in).
	table string
	// source is the FROM expression the selection reads, exposing id,
	// repository_id, deleted_at, created_at and every column the expressions
	// below use. Empty means the table itself.
	source string
	// nameCol is the package name, matched against name_pattern.
	nameCol string
	// versionCol is the version shown to the immutable-release guard; empty
	// means the `version` column.
	versionCol string
	// tsCol is the timestamp that ages a row (keep_since_days) and orders
	// versions (keep_last_n); empty means created_at.
	tsCol string
	// partitionCols identify "the same package" for keep_last_n: versions are
	// only ranked against other versions of the same name and architecture /
	// distribution, so a newer arm64 build never evicts the only amd64 one.
	partitionCols []string
	// versionExpr identifies one version; rows sharing it (e.g. a pypi sdist
	// and wheel, or the jar and pom of a maven release) count as a single
	// version for keep_last_n.
	versionExpr string
	// protectExpr is a boolean expression over source columns; rows for which
	// it holds still occupy their rank but are never selected (npm versions a
	// dist-tag points at, the newest build of a maven snapshot). Empty means
	// no row is protected.
	protectExpr string
	// blobCols are the (possibly nullable) FK columns to blobs.id owned by the row.
	blobCols []string
	// quotaTracked marks formats whose uploads reserve storage quota (one
	// artifact per row, the row's blob size). Deleting such a row must give that
	// usage back. Every hosted format is metered except docker tags.
	quotaTracked bool
	// kind is the trash kind of the table's rows, for the soft-delete path.
	kind models.TrashKind
}

// sourceExpr is the FROM expression of the selection.
func (t formatTable) sourceExpr() string {
	if t.source != "" {
		return t.source
	}

	return t.table
}

func (t formatTable) versionColumn() string {
	if t.versionCol != "" {
		return t.versionCol
	}

	return "version"
}

func (t formatTable) timeColumn() string {
	if t.tsCol != "" {
		return t.tsCol
	}

	return "created_at"
}

func (t formatTable) protection() string {
	if t.protectExpr != "" {
		return t.protectExpr
	}

	return "false"
}

// CleanupFormatSupported reports whether the cleanup worker can run policies
// for repositories of the given format; the API uses it to reject policies
// that could never run.
func CleanupFormatSupported(format string) bool {
	_, ok := cleanupFormatTable(format)

	return ok
}

// Source of the npm rows: a version is a row of npm_versions, its repository
// and name live on the package.
const npmCleanupSource = `(SELECT v.id, v.package_id, v.version, v.created_at, v.blob_id, v.deleted_at,
	np.repository_id, np.full_name
	FROM npm_versions v JOIN npm_packages np ON np.id = v.package_id)`

// Source of the docker rows: a tag, named by its full image name. A tag is
// aged by its last update: re-tagging (latest, a moving 1.x tag) is activity.
const dockerCleanupSource = `(SELECT t.id, t.repository_id, t.name, t.image, t.created_at, t.updated_at, t.deleted_at,
	CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END AS full_name
	FROM docker_tags t JOIN repositories r ON r.id = t.repository_id)`

// cleanupFormatTable is the single set of formats cleanup policies support and
// how each stores its rows. Every hosted format is listed.
func cleanupFormatTable(format string) (formatTable, bool) {
	switch format {
	case "rpm":
		return formatTable{table: "rpm_packages", nameCol: "name", partitionCols: []string{"name", "arch"},
			versionExpr: "version || '-' || release || ':' || epoch::text", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindRPM}, true
	case "deb":
		return formatTable{table: "deb_packages", nameCol: "name", partitionCols: []string{"name", "arch", "distribution_id::text"},
			versionExpr: "version", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindDeb}, true
	case "helm":
		return formatTable{table: "helm_charts", nameCol: "name", partitionCols: []string{"name"},
			versionExpr: "version", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindHelm}, true
	case "pypi":
		return formatTable{table: "pypi_packages", nameCol: "name", partitionCols: []string{"name"},
			versionExpr: "version", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindPyPI}, true
	case "nuget":
		return formatTable{table: "nuget_packages", nameCol: "package_id", partitionCols: []string{"package_id"},
			versionExpr: "version", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindNuGet}, true
	case "go": // the format id is "go"; the table is gomod_modules (chk_repo_format)
		return formatTable{table: "gomod_modules", nameCol: "module_path", partitionCols: []string{"module_path"},
			versionExpr: "version", blobCols: []string{"zip_blob_id", "mod_blob_id"}, quotaTracked: true, kind: models.TrashKindGoMod}, true
	case "npm":
		// A version a dist-tag points at is never selected: removing it would
		// leave the tag (latest, next, ...) dangling.
		return formatTable{table: "npm_versions", source: npmCleanupSource, nameCol: "full_name", partitionCols: []string{"package_id::text"},
			versionExpr: "version", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindNPM,
			protectExpr: "EXISTS (SELECT 1 FROM npm_dist_tags d WHERE d.package_id = src.package_id AND d.version = src.version)"}, true
	case "maven":
		return mavenCleanupTable(), true
	case "ivy":
		return formatTable{table: "ivy_artifacts", nameCol: "organisation || ':' || module", versionCol: "revision",
			partitionCols: []string{"organisation", "module"}, versionExpr: "revision",
			blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindIvy}, true
	case "sbt":
		return formatTable{table: "sbt_artifacts", nameCol: "group_id || ':' || artifact_id",
			partitionCols: []string{"group_id", "artifact_id", "scala_version"}, versionExpr: "version",
			blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindSBT}, true
	case "cargo":
		return formatTable{table: "cargo_crates", nameCol: "name", partitionCols: []string{"name"},
			versionExpr: "version", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindCargo}, true
	case "rubygems":
		return formatTable{table: "rubygems_gems", nameCol: "name", partitionCols: []string{"name", "platform"},
			versionExpr: "version", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindRubyGems}, true
	case "conda":
		// One version is one build; the .tar.bz2 and .conda files of a build
		// count together. Ranked per package and subdir (platform).
		return formatTable{table: "conda_packages", nameCol: "name", partitionCols: []string{"name", "platform"},
			versionExpr: "version || '-' || build_string", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindConda}, true
	case "conan":
		// Every revision of a recipe version ranks and goes as one version.
		return formatTable{table: "conan_packages", nameCol: "name", partitionCols: []string{"name"},
			versionExpr: "version || '@' || user_channel", blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindConan}, true
	case "raw":
		// A raw file has no version: files are ranked against the other files
		// of their directory, newest first.
		return formatTable{table: "raw_artifacts", nameCol: "path", versionCol: "''::text",
			partitionCols: []string{"regexp_replace(path, '[^/]*$', '')"}, versionExpr: "path",
			blobCols: []string{"blob_id"}, quotaTracked: true, kind: models.TrashKindRaw}, true
	case "docker":
		// Tags are what a policy removes; the manifests they leave untagged and
		// their layers are reclaimed by registry garbage collection, which a
		// cleanup run triggers once it removed tags.
		return formatTable{table: "docker_tags", source: dockerCleanupSource, nameCol: "full_name", versionCol: "name", tsCol: "updated_at",
			partitionCols: []string{"image"}, versionExpr: "name", kind: models.TrashKindDockerTag}, true
	default:
		return formatTable{}, false
	}
}

// mavenCleanupTable ranks releases against the other releases of their
// artifact, and the timestamped builds of each snapshot version against each
// other (a build is a version of its own). The newest build of a snapshot is
// kept whatever its age: it is what clients resolve -SNAPSHOT to.
func mavenCleanupTable() formatTable {
	return formatTable{
		table:   "maven_artifacts",
		nameCol: "group_id || ':' || artifact_id_coord",
		partitionCols: []string{
			"group_id", "artifact_id_coord", "is_snapshot::text",
			"CASE WHEN is_snapshot THEN version ELSE '' END",
		},
		versionExpr: "version || COALESCE('#' || snapshot_timestamp || '-' || snapshot_build_number::text, '')",
		protectExpr: `is_snapshot AND DENSE_RANK() OVER (PARTITION BY group_id, artifact_id_coord, version
			ORDER BY snapshot_timestamp DESC NULLS LAST, snapshot_build_number DESC) = 1`,
		blobCols:     []string{"blob_id"},
		quotaTracked: true,
		kind:         models.TrashKindMaven,
	}
}

// blobSQL returns the select-list fragment reading the row's blob ids as text
// (", COALESCE(col::text, ”) AS b0, ...") and the fragment re-selecting them
// from a subquery (", b0, ..."). Both are empty for a format without blobs.
func (t formatTable) blobSQL() (string, string) {
	var s, o strings.Builder

	for i, c := range t.blobCols {
		fmt.Fprintf(&s, ", COALESCE(%s::text, '') AS b%d", c, i)
		fmt.Fprintf(&o, ", b%d", i)
	}

	return s.String(), o.String()
}
