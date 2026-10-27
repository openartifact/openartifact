// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"strings"
)

// catalogBranch maps one package table onto the shared shape of the package
// listing (see ListRepoComponents in queries/components.sql, whose branches
// these are). The catalog queries that cannot be a static sqlc statement (they
// are cut per repository format, or bind a variable set of repositories) build
// their union from this list; TestCatalogBranchesCoverListing keeps it in step
// with the listing, so a package table added there cannot silently vanish from
// the rollup, the stats and the recent list.
type catalogBranch struct {
	// format is the listed format; empty for the proxy cache, whose rows take
	// the format of their repository.
	format string
	// from is the FROM list; t is the package row. Joins here must not fan out.
	from string
	// repo is the repository id expression.
	repo string
	// live is the predicate that selects the rows the listing shows.
	live string
	// name and version are the listed name and version expressions.
	name    string
	version string
	// blob is the blob id expression and blobKey the sha256 of a proxy cache
	// entry's blob; at most one is not NULL.
	blob    string
	blobKey string
	// prefSize and fbSize are the size rules of the listing: prefSize wins over
	// the blob's size, fbSize is the fallback when no blob is found.
	prefSize string
	fbSize   string
	// sizeFrom joins what blob and prefSize read beyond from. It is only
	// needed where the blob is resolved, so the narrow rows skip it.
	sizeFrom string
}

const (
	nullUUID   = "NULL::uuid"
	nullBigint = "NULL::bigint"
	liveRow    = "t.deleted_at IS NULL"
)

// simpleBranch is the usual shape: one table, one row per listed item.
func simpleBranch(format, table, name, version, blob string) catalogBranch {
	return catalogBranch{
		format: format, from: table + " t", repo: "t.repository_id", live: liveRow,
		name: name, version: version, blob: blob, blobKey: "NULL::text",
		prefSize: nullBigint, fbSize: nullBigint,
	}
}

// catalogBranches are the package tables of the listing, one per format, and
// the proxy cache.
func catalogBranches() []catalogBranch {
	conan := simpleBranch("conan", "conan_packages", "t.name", "t.version", "t.blob_id")
	// One row per recipe revision: its manifest, or a pre-0.5.0 row without a file name.
	conan.live = "t.deleted_at IS NULL AND t.package_id = '' AND t.filename IN ('', 'conanmanifest.txt')"

	rpm := simpleBranch("rpm", "rpm_packages", "t.name", "t.version", "t.blob_id")
	rpm.prefSize = "t.package_size::bigint"

	gems := simpleBranch("rubygems", "rubygems_gems", "t.name", "t.version", "t.blob_id")
	gems.prefSize = "NULLIF(t.size, 0)::bigint"

	npm := simpleBranch("npm", "npm_versions", "np.full_name", "t.version", "t.blob_id")
	npm.from = "npm_versions t JOIN npm_packages np ON np.id = t.package_id"
	npm.repo = "np.repository_id"

	docker := simpleBranch(
		"docker",
		"docker_tags",
		"(CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)",
		"t.name",
		"dm.blob_id",
	)
	docker.from = "docker_tags t JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL"
	docker.sizeFrom = "LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest " +
		"= t.manifest_digest AND dm.deleted_at IS NULL"
	docker.prefSize = "COALESCE(dm.size, 0)::bigint"

	// The proxy cache: a cached upstream file; name is its upstream-relative path.
	proxy := simpleBranch("", "proxy_cache_entries", "t.cache_key", "''", nullUUID)
	proxy.from = "proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id"
	proxy.live = "r.deleted_at IS NULL AND t.status_code = 200 AND t.blob_key IS NOT NULL"
	proxy.blobKey = "t.blob_key::text"
	proxy.fbSize = "t.size_bytes::bigint"

	return []catalogBranch{
		simpleBranch("deb", "deb_packages", "t.name", "t.version", "t.blob_id"),
		rpm,
		npm,
		simpleBranch("pypi", "pypi_packages", "t.name", "t.version", "t.blob_id"),
		simpleBranch("maven", "maven_artifacts", "t.group_id || ':' || t.artifact_id_coord", "t.version", "t.blob_id"),
		simpleBranch("ivy", "ivy_artifacts", "t.organisation || ':' || t.module", "t.revision", "t.blob_id"),
		simpleBranch("sbt", "sbt_artifacts", "t.group_id || ':' || t.artifact_id", "t.version", "t.blob_id"),
		simpleBranch("nuget", "nuget_packages", "t.package_id", "t.version", "t.blob_id"),
		simpleBranch("go", "gomod_modules", "t.module_path", "t.version", "COALESCE(t.zip_blob_id, t.mod_blob_id)"),
		simpleBranch("cargo", "cargo_crates", "t.name", "t.version", "t.blob_id"),
		gems,
		simpleBranch("conda", "conda_packages", "t.name", "t.version", "t.blob_id"),
		conan,
		simpleBranch("apk", "apk_packages", "t.branch || '/' || t.repo || '/' || t.name", "t.version", "t.blob_id"),
		simpleBranch("terraform", "terraform_modules", "'modules/' || t.name || '/' || t.system", "t.version", "t.blob_id"),
		simpleBranch("terraform", "terraform_providers", "'providers/' || t.type", "t.version", "t.blob_id"),
		simpleBranch("helm", "helm_charts", "t.name", "t.version", "t.blob_id"),
		simpleBranch("raw", "raw_artifacts", "t.path", "''", "t.blob_id"),
		docker,
		proxy,
	}
}

// branchesForFormat returns the branches that can hold rows of a repository of
// the given format: its own table and the proxy cache. A format with no table
// of its own keeps every branch.
func branchesForFormat(repoFormat string) []catalogBranch {
	all := catalogBranches()

	if repoFormat == "" {
		return all
	}

	var kept []catalogBranch

	for _, b := range all {
		if b.format == repoFormat || b.format == "" {
			kept = append(kept, b)
		}
	}

	if len(kept) == 1 {
		return all
	}

	return kept
}

// formatExpr is the SQL expression of the listed format of a branch.
func (b catalogBranch) formatExpr() string {
	if b.format == "" {
		return "r.format::text"
	}

	return "'" + b.format + "'::text"
}

// narrow renders the branch as the narrow listing row: repository, id, format,
// name, version and the two timestamps, restricted by where (a predicate, or "").
func (b catalogBranch) narrow(where string) string {
	return fmt.Sprintf(
		`SELECT %s AS repository_id, t.id AS id, %s AS format, %s::text AS name, %s::text AS version, t.created_at AS created_at,
			t.updated_at AS updated_at
		FROM %s WHERE %s%s`,
		b.repo,
		b.formatExpr(),
		b.name,
		b.version,
		b.from,
		b.live,
		andPredicate(where),
	)
}

// wide renders the branch with what the size of a row is computed from: the
// blob id or key and the size rules of the listing.
func (b catalogBranch) wide(where string) string {
	return fmt.Sprintf(`SELECT t.id AS id, %s AS format, %s::text AS name, %s::text AS version,
			%s::uuid AS blob_id, %s::text AS blob_key, %s AS pref_size, %s AS fb_size
		FROM %s %s WHERE %s%s`,
		b.formatExpr(), b.name, b.version, b.blob, b.blobKey, b.prefSize, b.fbSize,
		b.from, b.sizeFrom, b.live, andPredicate(where))
}

func andPredicate(where string) string {
	if where == "" {
		return ""
	}

	return " AND " + where
}

// unionOf joins the SQL of every branch with UNION ALL, each as render(b).
func unionOf(branches []catalogBranch, render func(catalogBranch) string) string {
	parts := make([]string, len(branches))
	for i, b := range branches {
		parts[i] = "(" + render(b) + ")"
	}

	return strings.Join(parts, "\n\t\tUNION ALL\n\t\t")
}
