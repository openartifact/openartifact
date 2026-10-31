// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"fmt"
	"strings"
)

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

// rollupArgs are the placeholders ("$n", or "" when the filter is off) of the
// optional filters of rollupQuery.
type rollupArgs struct {
	format  string
	name    string
	pattern string
}

// containsPattern is the ILIKE pattern of a case-insensitive substring match of
// q: the LIKE wildcards and the escape character of q are escaped, so a `%` or
// `_` in the search term matches itself.
func containsPattern(q string) string {
	var b strings.Builder

	b.Grow(len(q) + 2)
	b.WriteByte('%')

	for i := range len(q) {
		if c := q[i]; c == '\\' || c == '%' || c == '_' {
			b.WriteByte('\\')
		}

		b.WriteByte(q[i])
	}

	b.WriteByte('%')

	return b.String()
}

// uuidText matches the text form of a uuid (the artifact_path of a package row).
const uuidText = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

// rollupQuery is the first stage of the per-package rollup of a repository: the
// page of packages (format, name) with their latest version, version count and
// timestamps, and the total number of packages. Sizes and security come from
// the second stage (rollupSizesQuery) for the page's packages only.
//
// The hosted packages are grouped at the version level (package_versions, one
// row per live version) instead of the file level: a version is live while it
// has a live file (trash marks a version deleted when its last file goes), so
// the groups are the same at a fraction of the rows. created_at, the sort key,
// is the creation time of the newest matching live version; updated_at is the
// newer of the versions' updated_at and the newest file of the latest version.
// The latest version's name and file time are read for the page's packages only.
// Docker tags and the proxy cache keep the row-level grouping (they have no
// package table).
//
// Argument $1 is the repository; the others follow in args.
func rollupQuery(opts rollupFilter) (string, []any) {
	args := []any{opts.repoID}

	bind := func(v any) string {
		args = append(args, v)

		return fmt.Sprintf("$%d", len(args))
	}

	var a rollupArgs

	if opts.format != "" {
		a.format = bind(opts.format)
	}

	if opts.name != "" {
		a.name = bind(opts.name)
	}

	if opts.query != "" {
		a.pattern = bind(containsPattern(opts.query))
	}

	var (
		ctes, parts []string
		hosted      bool
		rowBranches []catalogBranch
	)

	for _, b := range branchesForFormat(opts.repoFormat) {
		if b.scope == scopeHosted {
			hosted = true
		} else {
			rowBranches = append(rowBranches, b)
		}
	}

	if opts.flagged {
		ctes = append(ctes, "flagged_ids AS MATERIALIZED ("+flaggedArtifactIDs("$1")+")")
	}

	if hosted {
		ctes = append(ctes, hostedRollupCTEs(a, opts.flagged)...)
		parts = append(
			parts,
			"SELECT format, name, version_count, created_at, updated_at, package_id, NULL::text AS latest_version FROM hosted",
		)
	}

	if len(rowBranches) > 0 {
		ctes = append(ctes, rowRollupCTEs(rowBranches, a, opts.flagged)...)
		parts = append(
			parts,
			"SELECT format, name, version_count, created_at, updated_at, NULL::uuid AS package_id, latest_version FROM rowgrouped",
		)
	}

	order, outer := "created_at DESC, format, name", "p.created_at DESC, p.format, p.name"
	if opts.sortByName {
		order, outer = "name, format", "p.name, p.format"
	}

	ctes = append(ctes, fmt.Sprintf(`page AS (
		SELECT g.*, count(*) OVER () AS total FROM (
			%s
		) g
		ORDER BY %s
		LIMIT %s OFFSET %s
	)`, strings.Join(parts, "\n\t\t\tUNION ALL\n\t\t\t"), order, bind(opts.limit), bind(opts.offset)))

	return fmt.Sprintf(`WITH %s
	SELECT p.format, p.name, COALESCE(p.latest_version, lv.version), p.version_count, p.created_at,
	       GREATEST(p.updated_at, lv.file_updated_at), p.total
	FROM page p
	LEFT JOIN LATERAL (
		SELECT v.version, (SELECT max(f.updated_at) FROM package_files f
			WHERE f.version_id = v.id AND f.deleted_at IS NULL AND f.listed) AS file_updated_at
		FROM package_versions v
		WHERE p.package_id IS NOT NULL AND v.package_id = p.package_id AND v.deleted_at IS NULL AND v.created_at = p.created_at%s
		ORDER BY v.id LIMIT 1
	) lv ON true
	ORDER BY %s`,
		strings.Join(ctes, ",\n\t"), latestMatch(a), outer), args
}

// latestMatch is the predicate that keeps, among the live versions created at
// the group's newest time, the ones the q filter matches (the group's newest
// matching version, which the filter picked the created_at of).
func latestMatch(a rollupArgs) string {
	if a.pattern == "" {
		return ""
	}

	return fmt.Sprintf(" AND (p.name ILIKE %[1]s OR v.version ILIKE %[1]s)", a.pattern)
}

// hostedRollupCTEs groups the live versions of the repository that match the
// filters per package (CTE hosted). A name or version search is the union of two
// trigram-index lookups (the package name, the version) instead of one OR over
// a join, which no index serves. In flagged mode only the packages that have a
// flagged file are read, and a package stays when its newest version is one of
// the flagged versions.
func hostedRollupCTEs(a rollupArgs, flagged bool) []string {
	extra := ""
	if a.format != "" {
		extra += " AND pk.format = " + a.format
	}

	if a.name != "" {
		extra += " AND pk.name = " + a.name
	}

	if flagged {
		extra += " AND v.package_id IN (SELECT package_id FROM fpk)"
	}

	const cols = "v.id, v.package_id, v.created_at, v.updated_at"

	// The package row is read for a format or name filter only.
	versionFrom := "package_versions v"
	if a.format != "" || a.name != "" {
		versionFrom += " JOIN packages pk ON pk.id = v.package_id"
	}

	byVersion := fmt.Sprintf("SELECT %s FROM %s WHERE v.repository_id = $1 AND v.deleted_at IS NULL%s", cols, versionFrom, extra)
	matched := byVersion

	if a.pattern != "" {
		byName := fmt.Sprintf(
			"SELECT %s FROM packages pk JOIN package_versions v ON v.package_id = pk.id WHERE pk.repository_id = $1 AND v.deleted_at IS NULL%s",
			cols,
			extra,
		)
		matched = fmt.Sprintf("%[1]s AND v.version ILIKE %[3]s\n\t\tUNION\n\t\t%[2]s AND pk.name ILIKE %[3]s", byVersion, byName, a.pattern)
	}

	var ctes []string

	aggExtra, keep := "", ""

	if flagged {
		ctes = append(ctes,
			fmt.Sprintf(`fv AS MATERIALIZED (
		SELECT DISTINCT f.version_id AS id FROM package_files f
		JOIN (SELECT CASE WHEN fa.artifact_path ~* '%s' THEN fa.artifact_path::uuid END AS fid FROM flagged_ids fa) x ON x.fid = f.id
		WHERE f.repository_id = $1 AND f.deleted_at IS NULL AND f.listed
	)`, uuidText),
			"fpk AS MATERIALIZED (SELECT DISTINCT v.package_id FROM package_versions v WHERE v.id IN (SELECT id FROM fv))")

		aggExtra = ", (array_agg(id ORDER BY created_at DESC, id))[1] AS latest_id"
		keep = " WHERE a.latest_id IN (SELECT id FROM fv)"
	}

	return append(ctes,
		fmt.Sprintf(`hv AS (
		%s
	)`, matched),
		fmt.Sprintf(`agg AS (
		SELECT package_id, count(*) AS version_count, max(created_at) AS created_at, max(updated_at) AS updated_at%s
		FROM hv GROUP BY package_id
	)`, aggExtra),
		fmt.Sprintf(`hosted AS (
		SELECT pk.format::text AS format, pk.name::text AS name, a.version_count, a.created_at, a.updated_at, a.package_id
		FROM agg a JOIN packages pk ON pk.id = a.package_id%s
	)`, keep))
}

// rowRollupCTEs groups the rows of the docker and proxy branches (no package
// table): per (format, name) the latest version, the distinct version count and
// the timestamps (CTE rowgrouped).
func rowRollupCTEs(branches []catalogBranch, a rollupArgs, flagged bool) []string {
	var conds []string

	if a.format != "" {
		conds = append(conds, "u.format = "+a.format)
	}

	if a.name != "" {
		conds = append(conds, "u.name = "+a.name)
	}

	if a.pattern != "" {
		conds = append(conds, fmt.Sprintf("(u.name ILIKE %[1]s OR u.version ILIKE %[1]s)", a.pattern))
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	rows := unionOf(branches, func(b catalogBranch) string { return b.narrow(b.repo + " = $1") })

	flaggedSel, flaggedWhere := "", ""
	if flagged {
		flaggedSel = ", bool_or(r.version = l.latest_version AND r.id::text IN (SELECT artifact_path FROM flagged_ids)) AS flagged"
		flaggedWhere = "WHERE flagged"
	}

	return []string{
		fmt.Sprintf(`u AS (
		SELECT u.repository_id, u.id, u.format, u.name, u.version, u.created_at, u.updated_at FROM (
		%s
		) u %s
	)`, rows, where),
		`latest AS (
		SELECT DISTINCT ON (format, name) format, name, version AS latest_version
		FROM u
		ORDER BY format, name, created_at DESC, id
	)`,
		fmt.Sprintf(`rowgrouped AS (
		SELECT format, name, version_count, created_at, updated_at, latest_version FROM (
			SELECT r.format, r.name, l.latest_version,
			       count(DISTINCT r.version) AS version_count,
			       max(r.created_at) AS created_at, max(r.updated_at) AS updated_at%s
			FROM u r JOIN latest l ON l.format = r.format AND l.name = r.name
			GROUP BY r.format, r.name, l.latest_version
		) g %s
	)`, flaggedSel, flaggedWhere),
	}
}
