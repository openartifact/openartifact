// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// dockerFormat is the one format the cleanup engine special-cases: it keeps
// dedicated tables (tags are what a policy removes) and has no storage
// descriptor.
const dockerFormat = "docker"

// cleanupTable says what the cleanup engine ranks and removes in a repository
// of one format. Every package format except docker shares package_files: the
// candidates are its live, listed files, ranked per the format's storage.Cleanup
// descriptor and removed through the generic trash path.
type cleanupTable struct {
	format string
	// kind is the trash kind of the rows, for the soft-delete path.
	kind models.TrashKind
	// docker selects the dedicated docker_tags code path.
	docker bool
	// cleanup is the ranking descriptor of a package format.
	cleanup storage.Cleanup
	// scope is how many files one candidate stands for.
	scope storage.TrashScope
}

// cleanupTableFor returns the table of a format, false when cleanup policies do
// not support it (no storage descriptor and not docker).
func cleanupTableFor(format string) (cleanupTable, bool) {
	if format == dockerFormat {
		// Tags are what a policy removes; the manifests they leave untagged and
		// their layers are reclaimed by registry garbage collection, which a
		// cleanup run triggers once it removed tags.
		return cleanupTable{format: format, kind: models.TrashKindDockerTag, docker: true}, true
	}

	s, ok := storage.Lookup(format)
	if !ok {
		return cleanupTable{}, false
	}

	return cleanupTable{
		format:  format,
		kind:    models.TrashKind(s.Trash.Kind),
		cleanup: s.Cleanup,
		scope:   s.Trash.Scope,
	}, true
}

// CleanupFormatSupported reports whether the cleanup worker can run policies
// for repositories of the given format; the API uses it to reject policies
// that could never run.
func CleanupFormatSupported(format string) bool {
	_, ok := cleanupTableFor(format)

	return ok
}

// candidateSQL are the pieces of the candidate query: it ranks the rows of one
// repository and returns the ones a policy may remove.
type candidateSQL struct {
	// id, name, version, pkg, blobs and artifacts are the select-list
	// expressions of the candidate columns (id text, name text, version text,
	// package id text, comma-separated blob ids text, artifacts int).
	id, name, version, pkg, blobs, artifacts string
	// aged is the timestamp that ages a row (keep_since_days); ranked the one
	// that orders versions (keep_last_n).
	aged, ranked string
	// protect is the boolean expression of rows that are never selected.
	protect string
	// partition are the expressions of "the same package"; versionExpr identifies
	// one version inside it.
	partition   []string
	versionExpr string
	// from is the FROM clause with its WHERE clause; $1 is the repository id.
	from string
}

// query renders the ranking query. conds are ANDed over the ranked rows.
//
// Inner: the newest timestamp per (partition, version) so that multi-file
// versions rank as one. Middle: dense rank of versions within a partition.
// Outer: protected rows stay ranked (they occupy a slot) but are never selected.
func (c candidateSQL) query(conds []string) string {
	return fmt.Sprintf(`
		SELECT id, cname, cver, pid, blobs, artifacts FROM (
			SELECT id, cname, cver, pid, blobs, artifacts, created_at, prot,
			       DENSE_RANK() OVER (PARTITION BY %[1]s ORDER BY vts DESC, ver DESC) AS rk
			FROM (
				SELECT %[2]s AS id, %[3]s AS cname, %[4]s AS cver, %[5]s AS pid, %[6]s AS blobs, %[7]s AS artifacts,
				       %[8]s AS created_at,
				       (%[9]s) AS prot,
				       %[10]s,
				       (%[11]s) AS ver,
				       MAX(%[12]s) OVER (PARTITION BY %[13]s, (%[11]s)) AS vts
				%[14]s
			) base
		) ranked
		WHERE %[15]s`,
		partitionAliases(len(c.partition)),
		c.id, c.name, c.version, c.pkg, c.blobs, c.artifacts,
		c.aged,
		c.protect,
		partitionSelect(c.partition),
		c.versionExpr,
		c.ranked,
		strings.Join(c.partition, ", "),
		c.from,
		strings.Join(conds, " AND "))
}

// filesFrom is the FROM/WHERE of the package_files candidates: the live, listed
// files of one repository with their version and package.
const filesFrom = `FROM package_files f
				JOIN package_versions v ON v.id = f.version_id
				JOIN packages p ON p.id = v.package_id
				WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.listed`

// blobIDsSQL is the select expression of the comma-separated ids of the blobs
// one candidate holds: its own file, or with ScopeVersion every live file of
// its version (the go.mod goes with the zip).
func blobIDsSQL(scope storage.TrashScope) string {
	if scope == storage.ScopeVersion {
		return `COALESCE((SELECT string_agg(sf.blob_id::text, ',') FROM package_files sf
					WHERE sf.version_id = f.version_id AND sf.deleted_at IS NULL AND sf.blob_id IS NOT NULL), '')`
	}

	return `COALESCE(f.blob_id::text, '')`
}

// sql is the candidate query pieces of the table.
func (t cleanupTable) sql() candidateSQL {
	if t.docker {
		// A tag is named by its full image name and aged by its last update:
		// re-tagging (latest, a moving 1.x tag) is activity.
		return candidateSQL{
			id: "t.id::text", name: "CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END",
			version: "t.name", pkg: "''::text", blobs: "''::text", artifacts: "0",
			aged: "t.updated_at", ranked: "t.updated_at", protect: "false",
			partition: []string{"t.image"}, versionExpr: "t.name",
			from: `FROM docker_tags t JOIN repositories r ON r.id = t.repository_id
				WHERE t.repository_id = $1 AND t.deleted_at IS NULL`,
		}
	}

	return candidateSQL{
		id: "f.id::text", name: "p.name", version: "v.version", pkg: "p.id::text",
		blobs: blobIDsSQL(t.scope), artifacts: "(f.role = 'primary')::int",
		aged: "f.created_at", ranked: "v.created_at",
		protect:   t.cleanup.ProtectSQL(),
		partition: t.cleanup.PartitionExprs(), versionExpr: t.cleanup.VersionSQL(),
		from: filesFrom,
	}
}

// idColumn is the qualified id column the candidates of the table are keyed by.
func (t cleanupTable) idColumn() string {
	if t.docker {
		return "t.id"
	}

	return "f.id"
}

func partitionAliases(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("p%d", i)
	}

	return strings.Join(parts, ", ")
}

func partitionSelect(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprintf("%s AS p%d", c, i)
	}

	return strings.Join(parts, ", ")
}
