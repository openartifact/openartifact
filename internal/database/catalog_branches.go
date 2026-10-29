// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"strings"
)

// catalogBranch maps one source of package rows onto the shared shape of the
// package listing (see ListRepoComponents in queries/components.sql, whose
// branches these are): the generic package_files/versions/packages tables that
// hold every format but docker, the docker tags and the proxy cache. The
// catalog queries that cannot be a static sqlc statement (they are cut per
// repository format, or bind a variable set of repositories) build their union
// from this list, so what the listing shows is what the rollup, the stats and
// the recent list count.
type catalogBranch struct {
	// scope says which repositories can hold rows of the branch.
	scope branchScope
	// formatSQL is the SQL expression of the listed format.
	formatSQL string
	// from is the FROM list; t is the row. Joins here must not fan out.
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
	// packageCount is the SQL expression (a scalar subquery of the repository
	// row cr) of the number of distinct package names of a repository, when a
	// cheaper source than counting the rows of from exists; "" counts the
	// distinct names over from.
	packageCount string
}

// branchScope is the set of repositories a branch can hold rows of.
type branchScope int

const (
	// scopeHosted: every format but docker (the generic package tables).
	scopeHosted branchScope = iota
	// scopeDocker: docker repositories (the docker tag tables).
	scopeDocker
	// scopeProxy: any repository (the proxy cache).
	scopeProxy
)

const (
	nullUUID   = "NULL::uuid"
	nullBigint = "NULL::bigint"
	dockerFmt  = "docker"
)

// hostedBranch is every format but docker: one row per listed live file of the
// generic tables. A file's size is its own size_bytes when the format records
// one, else its blob's.
func hostedBranch() catalogBranch {
	return catalogBranch{
		scope:     scopeHosted,
		formatSQL: "pk.format::text",
		from: "package_files t JOIN package_versions v ON v.id = t.version_id " +
			"JOIN packages pk ON pk.id = v.package_id",
		repo: "t.repository_id", live: "t.deleted_at IS NULL AND t.listed",
		name: "pk.name", version: "v.version", blob: "t.blob_id", blobKey: "NULL::text",
		prefSize: "t.size_bytes::bigint", fbSize: nullBigint,
		// The packages table has one row per package; a package with no live
		// version is not listed (every file of it is trashed).
		packageCount: `(SELECT count(*) FROM packages pk WHERE pk.repository_id = cr.id
			AND EXISTS (SELECT 1 FROM package_versions v WHERE v.package_id = pk.id AND v.deleted_at IS NULL))`,
	}
}

// dockerBranch is one row per docker tag; the registry image name is the
// repository name plus the image path inside it.
func dockerBranch() catalogBranch {
	return catalogBranch{
		scope:     scopeDocker,
		formatSQL: "'" + dockerFmt + "'::text",
		from:      "docker_tags t JOIN repositories r ON r.id = t.repository_id AND r.deleted_at IS NULL",
		repo:      "t.repository_id", live: "t.deleted_at IS NULL",
		name:    "(CASE WHEN t.image = '' THEN r.name ELSE r.name || '/' || t.image END)",
		version: "t.name", blob: "dm.blob_id", blobKey: "NULL::text",
		prefSize: "COALESCE(dm.size, 0)::bigint", fbSize: nullBigint,
		sizeFrom: "LEFT JOIN docker_manifests dm ON dm.repository_id = t.repository_id AND dm.image = t.image AND dm.digest " +
			"= t.manifest_digest AND dm.deleted_at IS NULL",
	}
}

// proxyBranch is the proxy cache: a cached upstream file; name is its
// upstream-relative path.
func proxyBranch() catalogBranch {
	return catalogBranch{
		scope:     scopeProxy,
		formatSQL: "r.format::text",
		from:      "proxy_cache_entries t JOIN repositories r ON r.id = t.repository_id",
		repo:      "t.repository_id",
		live:      "r.deleted_at IS NULL AND t.status_code = 200 AND t.blob_key IS NOT NULL",
		name:      "t.cache_key", version: "''", blob: nullUUID, blobKey: "t.blob_key::text",
		prefSize: nullBigint, fbSize: "t.size_bytes::bigint",
	}
}

// catalogBranches are the sources of the listing.
func catalogBranches() []catalogBranch {
	return []catalogBranch{hostedBranch(), dockerBranch(), proxyBranch()}
}

// branchesForFormat returns the branches that can hold rows of a repository of
// the given format: its own rows and the proxy cache. An empty format keeps
// every branch.
func branchesForFormat(repoFormat string) []catalogBranch {
	switch repoFormat {
	case "":
		return catalogBranches()
	case dockerFmt:
		return []catalogBranch{dockerBranch(), proxyBranch()}
	default:
		return []catalogBranch{hostedBranch(), proxyBranch()}
	}
}

// repoScope is the predicate on the repositories (alias cr) a branch applies
// to, or "" for every repository.
func (b catalogBranch) repoScope() string {
	switch b.scope {
	case scopeHosted:
		return "cr.format <> '" + dockerFmt + "'"
	case scopeDocker:
		return "cr.format = '" + dockerFmt + "'"
	default:
		return ""
	}
}

// formatExpr is the SQL expression of the listed format of a branch.
func (b catalogBranch) formatExpr() string { return b.formatSQL }

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
