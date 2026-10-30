// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"strings"
)

// The "flagged" rule of the catalog: an artifact is flagged when its latest scan
// of any type found a critical or high vulnerability or a secret, or is
// quarantined. The aggregates of queries/catalog_security.sql apply the same
// rule; this is its predicate form, for the filters (flagged=true).
//
// Only the latest job of each type counts (scan_jobs is unique per scanner, so
// a configuration change adds a job); the NOT EXISTS keeps the newest.
const (
	flaggedScanFrom = `scan_jobs fj
		LEFT JOIN vuln_results fv ON fj.scan_type = 'vuln' AND fv.scan_job_id = fj.id
		LEFT JOIN secret_results fs ON fj.scan_type = 'secret' AND fs.scan_job_id = fj.id
		LEFT JOIN license_results fl ON fj.scan_type = 'license' AND fl.scan_job_id = fj.id`

	flaggedScanWhere = `fj.artifact_ref_id = fr.id
		AND NOT EXISTS (SELECT 1 FROM scan_jobs fn WHERE fn.artifact_ref_id = fj.artifact_ref_id AND fn.scan_type = fj.scan_type
			AND fn.created_at > fj.created_at)
		AND (fv.critical_count + fv.high_count > 0 OR fv.quarantined OR fs.finding_count > 0 OR fs.quarantined OR fl.quarantined)`
)

// FlaggedArtifactPredicate is the SQL predicate "the package row idExpr of the
// repository repoExpr is flagged". Both are SQL expressions of the caller's query
// (idExpr is the row's uuid; artifact_refs.artifact_path holds it as text). It
// probes artifact_refs by its (repository_id, artifact_path) unique index once
// per row it is evaluated for, so apply it after the cheaper predicates.
func FlaggedArtifactPredicate(repoExpr, idExpr string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM artifact_refs fr, %s WHERE fr.repository_id = %s AND fr.artifact_path = %s::text AND %s)`,
		flaggedScanFrom, repoExpr, idExpr, flaggedScanWhere)
}

// flaggedArtifactIDs is the SQL of the artifact paths (package row ids as text)
// of the flagged artifacts of the repository bound to the placeholder repoParam.
// Where one repository is read this beats FlaggedArtifactPredicate: the scan
// tables are walked once instead of probed per package row.
func flaggedArtifactIDs(repoParam string) string {
	return fmt.Sprintf(`SELECT fr.artifact_path FROM artifact_refs fr, %s WHERE fr.repository_id = %s AND %s`,
		flaggedScanFrom, repoParam, flaggedScanWhere)
}

// rollupSizesQuery is the second stage of the rollup: every row (file) of the
// packages named in $2, with its version and its size by the rules of the
// listing. Row version and size let the caller pick the latest version's files.
// $1 is the repository.
func rollupSizesQuery(repoFormat string) string {
	rows := unionOf(branchesForFormat(repoFormat), func(b catalogBranch) string {
		return b.wide(b.repo + " = $1 AND " + b.name + " = ANY($2::text[])")
	})

	return fmt.Sprintf(`SELECT u.format, u.name, u.version, u.id::text,
		COALESCE(u.pref_size, bi.size_bytes, bs.size_bytes, u.fb_size, 0)::bigint AS size
	FROM (
		%s
	) u
	LEFT JOIN LATERAL (SELECT size_bytes FROM blobs WHERE id = u.blob_id) AS bi ON true
	LEFT JOIN LATERAL (SELECT size_bytes FROM blobs WHERE sha256 = u.blob_key) AS bs ON true`, rows)
}

// lastPublishedQuery is the time of the newest row of every repository in $1.
// Each branch is probed by its (repository_id, created_at DESC, id) index, and
// only for the repositories it applies to.
func lastPublishedQuery() string {
	parts := make([]string, 0, len(catalogBranches()))

	for _, b := range catalogBranches() {
		scope := ""
		if c := b.repoScope(); c != "" {
			scope = " WHERE " + c
		}

		parts = append(parts, fmt.Sprintf(
			`SELECT (SELECT t.created_at FROM %s WHERE %s AND %s = cr.id ORDER BY t.created_at DESC LIMIT 1) AS c%s`,
			b.from, b.live, b.repo, scope))
	}

	return `SELECT cr.id::text, (SELECT max(c) FROM (` + strings.Join(parts, "\n\t\tUNION ALL\n\t\t") + `) m)
	FROM repositories cr WHERE cr.id = ANY($1::uuid[])`
}

// packageCountQuery is the number of distinct package names of every repository
// in $1: one scalar subquery per branch and repository, so a repository costs
// what its own packages cost (the hosted branch counts packages rows, not the
// files of the repository) and a repository without rows costs an index probe.
func packageCountQuery() string {
	parts := make([]string, 0, len(catalogBranches()))

	for _, b := range catalogBranches() {
		count := b.packageCount
		if count == "" {
			count = fmt.Sprintf("(SELECT count(DISTINCT %s) FROM %s WHERE %s AND %s = cr.id)", b.name, b.from, b.live, b.repo)
		}

		if scope := b.repoScope(); scope != "" {
			count = fmt.Sprintf("(CASE WHEN %s THEN %s ELSE 0 END)", scope, count)
		}

		parts = append(parts, count)
	}

	return `SELECT cr.id::text, (` + strings.Join(parts, "\n\t\t+ ") + `)::bigint
	FROM repositories cr WHERE cr.id = ANY($1::uuid[])`
}

// recentPackagesQuery is the newest package versions of the repositories in $1:
// each branch contributes its newest $2 rows per repository it applies to (its
// (repository_id, created_at DESC, id) index serves that), and the union is cut
// to the newest $3 distinct (repository, name, version). Proxy cache entries
// are fetched files, not publications, so they are left out.
func recentPackagesQuery() string {
	var hosted []catalogBranch

	for _, b := range catalogBranches() {
		if b.scope != scopeProxy {
			hosted = append(hosted, b)
		}
	}

	rows := unionOf(hosted, func(b catalogBranch) string {
		return fmt.Sprintf(`SELECT cr.id AS repository_id, cr.name AS repository, x.format, x.name, x.version, x.created_at
			FROM repositories cr
			CROSS JOIN LATERAL (
				SELECT %s AS format, %s::text AS name, %s::text AS version, t.created_at AS created_at
				FROM %s WHERE %s AND %s = cr.id
				ORDER BY t.created_at DESC, t.id LIMIT $2
			) x
			WHERE cr.id = ANY($1::uuid[]) AND %s`,
			b.formatExpr(), b.name, b.version, b.from, b.live, b.repo, b.repoScope())
	})

	return `SELECT repository, format, name, version, max(created_at) AS created_at
	FROM (
		` + rows + `
	) u
	GROUP BY repository_id, repository, format, name, version
	ORDER BY max(created_at) DESC, repository, name, version
	LIMIT $3`
}
