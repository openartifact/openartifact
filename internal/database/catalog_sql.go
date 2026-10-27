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

// rollupQuery is the first stage of the per-package rollup of a repository: the
// page of packages (format, name) with their latest version, version count and
// timestamps, and the total number of packages. Sizes and security come from
// the second stage (rollupSizesQuery) for the page's packages only.
//
// Argument $1 is the repository; the others follow in args.
func rollupQuery(opts rollupFilter) (string, []any) {
	args := []any{opts.repoID}

	var conds []string

	bind := func(v any) string {
		args = append(args, v)

		return fmt.Sprintf("$%d", len(args))
	}

	if opts.format != "" {
		conds = append(conds, "u.format = "+bind(opts.format))
	}

	if opts.name != "" {
		conds = append(conds, "u.name = "+bind(opts.name))
	}

	if opts.query != "" {
		term := bind(opts.query)
		conds = append(conds, fmt.Sprintf("(strpos(lower(u.name), lower(%[1]s)) > 0 OR strpos(lower(u.version), lower(%[1]s)) > 0)", term))
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	branches := branchesForFormat(opts.repoFormat)
	rows := unionOf(branches, func(b catalogBranch) string { return b.narrow(b.repo + " = $1") })

	flaggedCTE, flaggedSel, flaggedHaving := "", "", ""
	if opts.flagged {
		flaggedCTE = ", flagged_ids AS MATERIALIZED (" + flaggedArtifactIDs("$1") + ")"
		flaggedSel = ", bool_or(r.version = l.latest_version AND r.id::text IN (SELECT artifact_path FROM flagged_ids)) AS flagged"
		flaggedHaving = "WHERE flagged"
	}

	order := "created_at DESC, format, name"
	if opts.sortByName {
		order = "name, format"
	}

	limitN := bind(opts.limit)
	offsetN := bind(opts.offset)

	return fmt.Sprintf(`WITH u AS (
		SELECT * FROM (
		%s
		) u %s
	), latest AS (
		SELECT DISTINCT ON (format, name) format, name, version AS latest_version
		FROM u
		ORDER BY format, name, created_at DESC, id
	)%s, grouped AS (
		SELECT r.format, r.name, l.latest_version,
		       count(DISTINCT r.version) AS version_count,
		       max(r.created_at) AS created_at, max(r.updated_at) AS updated_at%s
		FROM u r JOIN latest l ON l.format = r.format AND l.name = r.name
		GROUP BY r.format, r.name, l.latest_version
	)
	SELECT format, name, latest_version, version_count, created_at, updated_at, count(*) OVER () AS total
	FROM grouped
	%s
	ORDER BY %s
	LIMIT %s OFFSET %s`,
		rows, where, flaggedCTE, flaggedSel, flaggedHaving, order, limitN, offsetN), args
}

// rollupFilter is PackageRollupOptions resolved to what the query binds.
type rollupFilter struct {
	repoID     string
	repoFormat string
	format     string
	name       string
	query      string
	flagged    bool
	sortByName bool
	limit      int
	offset     int
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
// Each table is probed by its (repository_id, created_at DESC, id) index, and
// only for the repositories of its format.
func lastPublishedQuery() string {
	parts := make([]string, 0, len(catalogBranches()))

	for _, b := range catalogBranches() {
		scope := ""
		if b.format != "" {
			scope = " WHERE cr.format = '" + b.format + "'"
		}

		parts = append(parts, fmt.Sprintf(
			`SELECT (SELECT t.created_at FROM %s WHERE %s AND %s = cr.id ORDER BY t.created_at DESC LIMIT 1) AS c%s`,
			b.from, b.live, b.repo, scope))
	}

	return `SELECT cr.id::text, (SELECT max(c) FROM (` + strings.Join(parts, "\n\t\tUNION ALL\n\t\t") + `) m)
	FROM repositories cr WHERE cr.id = ANY($1::uuid[])`
}

// packageCountQuery is the number of distinct package names of every repository in $1.
func packageCountQuery() string {
	rows := unionOf(catalogBranches(), func(b catalogBranch) string {
		return fmt.Sprintf("SELECT %s AS repository_id, %s AS name FROM %s WHERE %s AND %s = ANY($1::uuid[])",
			b.repo, b.name, b.from, b.live, b.repo)
	})

	return `SELECT repository_id::text, count(DISTINCT name) FROM (
		` + rows + `
	) u GROUP BY repository_id`
}

// recentPackagesQuery is the newest package versions of the repositories in $1:
// each table contributes its newest $2 rows per repository of its format (its
// (repository_id, created_at DESC, id) index serves that), and the union is cut
// to the newest $3 distinct (repository, name, version). Proxy cache entries
// are fetched files, not publications, so they are left out.
func recentPackagesQuery() string {
	var hosted []catalogBranch

	for _, b := range catalogBranches() {
		if b.format != "" {
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
			WHERE cr.id = ANY($1::uuid[]) AND cr.format = '%s'`,
			b.formatExpr(), b.name, b.version, b.from, b.live, b.repo, b.format)
	})

	return `SELECT repository, format, name, version, max(created_at) AS created_at
	FROM (
		` + rows + `
	) u
	GROUP BY repository_id, repository, format, name, version
	ORDER BY max(created_at) DESC, repository, name, version
	LIMIT $3`
}
