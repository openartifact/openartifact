// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxSearchResults is the largest match count a search may have; broader
// queries are rejected so cost stays bounded.
const MaxSearchResults = 10000

type SearchResult struct {
	Repository string            `json:"repository"`
	Format     string            `json:"format"`
	Name       string            `json:"name"`
	Version    string            `json:"version"`
	Path       string            `json:"path"`
	SHA256     string            `json:"sha256"`
	Size       int64             `json:"size"`
	CreatedAt  time.Time         `json:"created_at"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// RepositoryResult is a repository whose own name matched the query (as
// opposed to SearchResult, which is a package/artifact inside a repository).
type RepositoryResult struct {
	Name        string `json:"name"`
	Format      string `json:"format"`
	Description string `json:"description"`
}

type Executor struct {
	db *sql.DB
}

func NewExecutor(db *sql.DB) *Executor {
	return &Executor{db: db}
}

// Every branch of the search view yields the same columns, in this order: the
// row's identity and sort key (repository_id, created_at, id), the fields shown
// in a result (format, name, version, path; group_id and artifact_id_coord are
// populated only for maven, scope only for npm) and the two locators of its
// size/checksum, blob_id (blobs row) and manifest_image/manifest_digest (docker: the digest and
// size come from the manifest).

// searchBlobJoins resolves a view row's sha256 and size. A row has either a
// blob_id (every format but docker) or a manifest_digest (docker), so the
// COALESCEs pick whichever join matched; rows with neither report ” and 0.
const searchBlobJoins = `LEFT JOIN blobs b ON b.id = %[1]s.blob_id
		LEFT JOIN docker_manifests dm ON dm.repository_id = %[1]s.repository_id AND dm.image = %[1]s.manifest_image AND dm.digest = %[1]s.manifest_digest AND dm.deleted_at IS NULL`

const (
	searchSHA256Expr = `COALESCE(b.sha256, regexp_replace(dm.digest, '^sha256:', ''), '')`
	searchSizeExpr   = `COALESCE(b.size_bytes, dm.size, 0)`
)

// narrowSearchView is the union of every format table reduced to
// searchColumns, without touching the blobs table: no row is widened by a join
// until the caller has filtered and paged the union.
func narrowSearchView() string {
	return `(` + strings.Join(unifiedSearchBranches(), "\n\tUNION ALL\n\t") + `)`
}

// searchSource returns the FROM source of the filter's predicates, aliased s,
// and whether the repositories table must be joined as r to evaluate them.
// Only a sha256 prefix needs checksums, so only then is the union enriched
// with them for every row; otherwise the filter, the count and the page
// selection all run on the narrow union and blobs is joined for one page only.
// repositories is joined only for a repo: filter: every row's repository_id
// already references an existing repository and is scoped in SQL to the
// readable set.
func searchSource(filter SearchFilter) (string, bool) {
	joinRepo := len(filter.Repos) > 0

	if filter.SHA256Prefix == "" {
		return narrowSearchView(), joinRepo
	}

	//nolint:unqueryvet // n.* is the narrow view above: its columns are the fixed, documented search columns
	return `(SELECT n.*, ` + searchSHA256Expr + ` AS sha256, ` + searchSizeExpr + ` AS size
		FROM ` + narrowSearchView() + ` AS n ` + fmt.Sprintf(searchBlobJoins, "n") + `)`, joinRepo
}

// searchBranch renders one branch of the view: alias, repository column and
// per-format expressions are given in searchColumns order.
func searchBranch(format, name, version, path, groupID, artifactID, blobID, manifestDigest, from string) string {
	manifestImage := "NULL"
	if manifestDigest != "NULL" {
		manifestImage = from + ".image"
	}

	return `SELECT ` + from + `.repository_id, '` + format + `'::text AS format, ` + name + `::text AS name, ` + version + `::text AS version,
			` + from + `.created_at, ` + from + `.id, ` + path + `::text AS path,
			` + groupID + `::text AS group_id, ` + artifactID + `::text AS artifact_id_coord, NULL::text AS scope,
			` + blobID + `::uuid AS blob_id, ` + manifestImage + `::text AS manifest_image, ` + manifestDigest + `::text AS manifest_digest`
}

func unifiedSearchBranches() []string {
	const null = "NULL"

	return []string{
		searchBranch("cargo", "cc.name", "cc.version", "cc.name || '-' || cc.version || '.crate'", null, null, "cc.blob_id", null, "cc") +
			"\n\t\tFROM cargo_crates cc WHERE cc.deleted_at IS NULL",
		searchBranch("conda", "cp.name", "cp.version", "''", null, null, "cp.blob_id", null, "cp") +
			"\n\t\tFROM conda_packages cp WHERE cp.deleted_at IS NULL",
		searchBranch("conan", "co.name", "co.version", "''", null, null, "co.blob_id", null, "co") +
			// One result per recipe revision: its manifest row, or a pre-0.5.0 row without a file name.
			"\n\t\tFROM conan_packages co WHERE co.deleted_at IS NULL AND co.package_id = '' AND co.filename IN ('', 'conanmanifest.txt')",
		searchBranch("docker", "dt.name", "''", "''", null, null, null, "dt.manifest_digest", "dt") +
			"\n\t\tFROM docker_tags dt WHERE dt.deleted_at IS NULL",
		// go: only the zip blob is reported (a module with just a .mod has none).
		searchBranch("go", "gm.module_path", "gm.version", "''", null, null, "gm.zip_blob_id", null, "gm") +
			"\n\t\tFROM gomod_modules gm WHERE gm.deleted_at IS NULL",
		searchBranch("helm", "hc.name", "hc.version", "hc.name || '-' || hc.version || '.tgz'", null, null, "hc.blob_id", null, "hc") +
			"\n\t\tFROM helm_charts hc WHERE hc.deleted_at IS NULL",
		searchBranch("ivy", "iv.organisation || '/' || iv.module", "iv.revision", "iv.artifact_name || '.' || iv.artifact_ext", null, null, "iv.blob_id", null, "iv") +
			"\n\t\tFROM ivy_artifacts iv WHERE iv.deleted_at IS NULL",
		searchBranch("maven", "ma.group_id || ':' || ma.artifact_id_coord", "ma.version", "ma.repo_path", "ma.group_id", "ma.artifact_id_coord", "ma.blob_id", null, "ma") +
			"\n\t\tFROM maven_artifacts ma WHERE ma.deleted_at IS NULL",
		// npm: id and created_at are the version's, repository and name the package's.
		`SELECT np.repository_id, 'npm'::text AS format, np.full_name::text AS name, nv.version::text AS version,
			nv.created_at, nv.id, nv.tarball_filename::text AS path,
			NULL::text AS group_id, NULL::text AS artifact_id_coord, NULLIF(np.scope, '')::text AS scope,
			nv.blob_id::uuid AS blob_id, NULL::text AS manifest_image, NULL::text AS manifest_digest
		FROM npm_packages np
		JOIN npm_versions nv ON nv.package_id = np.id
		WHERE nv.deleted_at IS NULL`,
		searchBranch("nuget", "ng.package_id", "ng.version", "ng.package_id || '.' || ng.version || '.nupkg'", null, null, "ng.blob_id", null, "ng") +
			"\n\t\tFROM nuget_packages ng WHERE ng.deleted_at IS NULL",
		searchBranch("pypi", "pp.name", "pp.version", "pp.filename", null, null, "pp.blob_id", null, "pp") +
			"\n\t\tFROM pypi_packages pp WHERE pp.deleted_at IS NULL",
		searchBranch("raw", "ra.path", "''", "ra.path", null, null, "ra.blob_id", null, "ra") +
			"\n\t\tFROM raw_artifacts ra WHERE ra.deleted_at IS NULL",
		searchBranch("rubygems", "rg.name", "rg.version", "rg.name || '-' || rg.version || '.gem'", null, null, "rg.blob_id", null, "rg") +
			"\n\t\tFROM rubygems_gems rg WHERE rg.deleted_at IS NULL",
		searchBranch("sbt", "sa.group_id || '/' || sa.artifact_id", "sa.version", "sa.filename", null, null, "sa.blob_id", null, "sa") +
			"\n\t\tFROM sbt_artifacts sa WHERE sa.deleted_at IS NULL",
		searchBranch("rpm", "rp.name", "rp.version", "COALESCE(rp.filename, '')", null, null, "rp.blob_id", null, "rp") +
			"\n\t\tFROM rpm_packages rp WHERE rp.deleted_at IS NULL",
		searchBranch("deb", "dp.name", "dp.version", "COALESCE(dp.filename, '')", null, null, "dp.blob_id", null, "dp") +
			"\n\t\tFROM deb_packages dp WHERE dp.deleted_at IS NULL",
	}
}

// CountResults returns how many rows the filter matches (capped at
// MaxSearchResults+1), scoped to the caller's readable repositories. An empty
// readableRepos set (no permission on anything) always yields 0 — it must never
// fall back to counting all rows. SearchPage already returns the count with its
// page; this stands alone for the one case that cannot (a page past the end).
func (e *Executor) CountResults(ctx context.Context, filter SearchFilter, readableRepos map[uuid.UUID]struct{}) (int64, error) {
	return e.countResults(ctx, filter, readableRepos, PageOptions{})
}

// countResults is CountResults for the page shape of opts: with GroupByPackage
// it counts packages instead of rows, except that a match set above
// MaxSearchResults rows is reported by its row count so the caller can reject
// the query exactly as for an ungrouped search.
func (e *Executor) countResults(ctx context.Context, filter SearchFilter, readableRepos map[uuid.UUID]struct{}, opts PageOptions) (int64, error) {
	if len(readableRepos) == 0 {
		return 0, nil
	}

	where, args := buildWhereClause(filter, readableRepos)

	source, joinRepo := searchSource(filter)

	// Bounded: callers only need to know whether the count exceeds
	// MaxSearchResults, so counting stops one row past it instead of
	// scanning every matching artifact.
	limit := strconv.Itoa(MaxSearchResults + 1)

	cols := "1"
	if opts.GroupByPackage {
		cols = "s.repository_id, s.format, s.name"
	}

	inner := `SELECT ` + cols + ` FROM ` + source + ` AS s` + repoJoin(joinRepo)
	if where != "" {
		inner += " WHERE " + where
	}

	q := `SELECT COUNT(*) FROM (` + inner + ` LIMIT ` + limit + `) AS c`

	if opts.GroupByPackage {
		q = `WITH m AS MATERIALIZED (` + inner + ` LIMIT ` + limit + `)
	SELECT CASE WHEN (SELECT count(*) FROM m) > ` + strconv.Itoa(MaxSearchResults) + `
		THEN (SELECT count(*) FROM m)
		ELSE (SELECT count(*) FROM (SELECT DISTINCT repository_id, format, name FROM m) AS g) END`
	}

	var count int64
	if err := e.db.QueryRowContext(ctx, q, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count query: %w", err)
	}

	return count, nil
}

func repoJoin(join bool) string {
	if !join {
		return ""
	}

	return " JOIN repositories r ON r.id = s.repository_id AND r.deleted_at IS NULL"
}

// Search runs the filter scoped to readableRepos. Repo scoping is enforced
// twice: at the SQL level (repository_id = ANY($n)) and again in Go after
// scanning, so a caller with no readable repositories always gets an empty
// result set even if the SQL-level filter were ever bypassed or misapplied.
//
// The query is staged: the page (best match first, then newest, id as final
// tiebreak so paging is stable; see relevanceExpr) is selected from the narrow
// union, and only those rows are joined to repositories (name) and blobs
// (sha256, size).
func (e *Executor) Search(
	ctx context.Context,
	filter SearchFilter,
	limit, offset int,
	readableRepos map[uuid.UUID]struct{},
) ([]SearchResult, error) {
	results, _, err := e.runSearch(ctx, filter, limit, offset, readableRepos, false, PageOptions{})

	return results, err
}

// PageOptions shapes the page SearchPage returns.
type PageOptions struct {
	// GroupByPackage returns one row per package (repository, format, name):
	// its latest version. Offset and total then count packages. A match set
	// above MaxSearchResults rows is still reported as too broad: the returned
	// total is then the capped row count, not a package count.
	GroupByPackage bool
}

// SearchPage is Search plus the number of matches, from one scan of the union.
// The matches are read once, capped at MaxSearchResults+1 rows (a total above
// MaxSearchResults is only ever used to reject the query), and both the page
// and the count come from that set; a count query beside a page query would
// scan the union twice. When the total exceeds MaxSearchResults the returned
// page is an arbitrary slice of the capped set and must be discarded by the
// caller. A page past the last match carries no row to read the total from, so
// that one case asks for it separately. The page is ranked only while the
// total is within the cap.
func (e *Executor) SearchPage(
	ctx context.Context,
	filter SearchFilter,
	opts PageOptions,
	limit, offset int,
	readableRepos map[uuid.UUID]struct{},
) ([]SearchResult, int64, error) {
	results, total, err := e.runSearch(ctx, filter, limit, offset, readableRepos, true, opts)
	if err != nil {
		return nil, 0, err
	}

	if len(results) == 0 && offset > 0 && len(readableRepos) > 0 {
		total, err = e.countResults(ctx, filter, readableRepos, opts)
		if err != nil {
			return nil, 0, err
		}
	}

	return results, total, nil
}

// runSearch builds and runs the staged page query. With capped the page is
// taken from the at most MaxSearchResults+1 matching rows and every returned
// row carries their count; otherwise the page is the best-ranked rows of all
// matches and the count is 0. opts.GroupByPackage requires capped.
func (e *Executor) runSearch(
	ctx context.Context,
	filter SearchFilter,
	limit, offset int,
	readableRepos map[uuid.UUID]struct{},
	capped bool,
	opts PageOptions,
) ([]SearchResult, int64, error) {
	if len(readableRepos) == 0 {
		return nil, 0, nil
	}

	where, args := buildWhereClause(filter, readableRepos)

	// The capped paths rank the already matched rows (and only when the set is
	// within the cap), the uncapped one ranks while matching.
	nameCol := "s.name"
	if capped {
		nameCol = "name"
	}

	rank, rankArgs := relevanceExpr(nameCol, relevanceTerm(filter), len(args)+1, true)
	args = append(args, rankArgs...)

	limitN := len(args) + 1
	offsetN := len(args) + 2
	args = append(args, limit, offset)

	source, joinRepo := searchSource(filter)

	const (
		// The columns of a match, unqualified: as the capped CTEs expose them.
		matchColumns = `repository_id, format, name, version, created_at, id, path,
			group_id, artifact_id_coord, scope, blob_id, manifest_image, manifest_digest`
		// The same columns qualified by the union alias, as the uncapped path reads them.
		sourceColumns = `s.repository_id, s.format, s.name, s.version, s.created_at, s.id, s.path,
			s.group_id, s.artifact_id_coord, s.scope, s.blob_id, s.manifest_image, s.manifest_digest`
	)

	matches := fmt.Sprintf(`FROM %s AS s%s
		%s`, source, repoJoin(joinRepo), whereClause(where))

	var pageCTE, totalExpr string

	switch {
	case capped && opts.GroupByPackage:
		// One row per package, its latest version; the window count then counts
		// packages. Above the cap nothing is grouped or ranked (that would sort
		// and regex-match thousands of rows to build a page that is discarded):
		// an arbitrary slice of the capped rows stands in, and every row of it
		// carries the capped row count, which is how the caller sees that the
		// query is too broad.
		pageCTE = fmt.Sprintf(`WITH matched AS MATERIALIZED (
		SELECT %[1]s, count(*) OVER () AS matched_total
		FROM (SELECT %[2]s %[3]s LIMIT %[4]d) AS capped
	), latest AS (
		SELECT DISTINCT ON (repository_id, format, name) %[1]s
		FROM matched
		WHERE matched_total <= %[7]d
		ORDER BY repository_id, format, name, created_at DESC, id
	), page AS (
		(SELECT %[1]s, %[8]s AS relevance, count(*) OVER () AS total
		FROM latest
		ORDER BY relevance, created_at DESC, id
		LIMIT $%[5]d OFFSET $%[6]d)
		UNION ALL
		(SELECT %[1]s, 0::int AS relevance, matched_total AS total
		FROM matched
		WHERE matched_total > %[7]d
		LIMIT $%[5]d)
	)`, matchColumns, sourceColumns, matches, MaxSearchResults+1, limitN, offsetN, MaxSearchResults, rank)
		totalExpr = "p.total"
	case capped:
		// The window count is evaluated before ORDER BY/LIMIT, over the capped
		// set; above the cap the rows are not ranked, as above.
		pageCTE = fmt.Sprintf(`WITH matched AS MATERIALIZED (
		SELECT %[1]s, count(*) OVER () AS matched_total
		FROM (SELECT %[2]s %[3]s LIMIT %[4]d) AS capped
	), page AS (
		SELECT %[1]s,
			CASE WHEN matched_total > %[7]d THEN 0::int ELSE %[8]s END AS relevance,
			matched_total AS total
		FROM matched
		ORDER BY relevance, created_at DESC, id
		LIMIT $%[5]d OFFSET $%[6]d
	)`, matchColumns, sourceColumns, matches, MaxSearchResults+1, limitN, offsetN, MaxSearchResults, rank)
		totalExpr = "p.total"
	default:
		pageCTE = fmt.Sprintf(`WITH page AS (
		SELECT %s, %s AS relevance
		%s
		ORDER BY relevance, s.created_at DESC, s.id
		LIMIT $%d OFFSET $%d
	)`, sourceColumns, rank, matches, limitN, offsetN)
		totalExpr = "0::bigint"
	}

	//nolint:gosec // G201: every formatted fragment is a constant or built from constants; user values are bound $n arguments
	q := fmt.Sprintf(`%s
	SELECT
		p.repository_id,
		r.name AS repository,
		p.format,
		p.name,
		p.version,
		p.path,
		%s AS sha256,
		%s AS size,
		p.created_at,
		p.group_id,
		p.artifact_id_coord,
		p.scope,
		%s AS total
	FROM page AS p
	JOIN repositories r ON r.id = p.repository_id AND r.deleted_at IS NULL
	%s
	ORDER BY p.relevance, p.created_at DESC, p.id`,
		pageCTE,
		searchSHA256Expr,
		searchSizeExpr,
		totalExpr,
		fmt.Sprintf(searchBlobJoins, "p"),
	)

	rows, err := e.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		results []SearchResult
		total   int64
	)

	for rows.Next() {
		var (
			repoIDRaw  string
			sr         SearchResult
			groupID    sql.NullString
			artifactID sql.NullString
			scope      sql.NullString
		)

		if err := rows.Scan(&repoIDRaw, &sr.Repository, &sr.Format, &sr.Name, &sr.Version,
			&sr.Path, &sr.SHA256, &sr.Size, &sr.CreatedAt, &groupID, &artifactID, &scope, &total); err != nil {
			return nil, 0, fmt.Errorf("scan search result: %w", err)
		}

		repoID, err := uuid.Parse(repoIDRaw)
		if err != nil {
			continue
		}

		if _, ok := readableRepos[repoID]; !ok {
			continue
		}

		sr.Metadata = map[string]string{}
		if groupID.Valid {
			sr.Metadata["groupId"] = groupID.String
		}

		if artifactID.Valid {
			sr.Metadata["artifactId"] = artifactID.String
		}

		if scope.Valid {
			sr.Metadata["scope"] = scope.String
		}

		results = append(results, sr)
	}

	return results, total, rows.Err()
}

// SearchRepositories returns the repositories themselves whose name matches the
// filter (case-insensitive substring, so "ui-kit" finds "ui-kit", "my-ui-kit-lib"
// and "ui-kit-docs"), best match first (exact name, prefix, substring; see
// relevanceExpr), then by name. The package-oriented terms of the filter
// apply to repositories as follows: name:/free text match the repository name,
// format: matches the repository format, repo: the repository name exactly.
// sha256: identifies artifacts, never a repository, so it yields no repositories.
// Like Search, the result is scoped to readableRepos in SQL and again in Go.
func (e *Executor) SearchRepositories(
	ctx context.Context,
	filter SearchFilter,
	limit, offset int,
	readableRepos map[uuid.UUID]struct{},
) ([]RepositoryResult, error) {
	if len(readableRepos) == 0 || filter.SHA256Prefix != "" {
		return nil, nil
	}

	var b whereBuilder

	b.addNameMatch("r.name", filter)

	if len(filter.Formats) > 0 {
		b.addIn("r.format", filter.Formats)
	}

	if len(filter.Repos) > 0 {
		b.addIn("r.name", filter.Repos)
	}

	b.addReadable("r.id", readableRepos)

	rank, rankArgs := relevanceExpr("r.name", relevanceTerm(filter), len(b.args)+1, false)
	b.args = append(b.args, rankArgs...)

	limitN := len(b.args) + 1
	b.args = append(b.args, limit, offset)

	//nolint:gosec // G201: the predicate is built from constant fragments and $n placeholders only; every user value is a bound argument
	q := fmt.Sprintf(`SELECT r.id, r.name, r.format, COALESCE(r.description, '')
	FROM repositories r
	WHERE r.deleted_at IS NULL AND %s
	ORDER BY %s, r.name, r.id
	LIMIT $%d OFFSET $%d`, b.String(), rank, limitN, limitN+1)

	rows, err := e.db.QueryContext(ctx, q, b.args...)
	if err != nil {
		return nil, fmt.Errorf("repository search query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []RepositoryResult

	for rows.Next() {
		var (
			repoIDRaw string
			rr        RepositoryResult
		)

		if err := rows.Scan(&repoIDRaw, &rr.Name, &rr.Format, &rr.Description); err != nil {
			return nil, fmt.Errorf("scan repository result: %w", err)
		}

		repoID, err := uuid.Parse(repoIDRaw)
		if err != nil {
			continue
		}

		if _, ok := readableRepos[repoID]; !ok {
			continue
		}

		results = append(results, rr)
	}

	return results, rows.Err()
}

// relevanceTerm is the text results are ranked against: the name: qualifier if
// given, else the free text. Empty means nothing to rank by (a format:, repo: or
// sha256: only query).
func relevanceTerm(filter SearchFilter) string {
	if filter.Name != "" {
		return filter.Name
	}

	return filter.FreeText
}

// relevanceExpr returns a SQL expression ranking nameExpr against term, lowest
// first: 0 exact (case-insensitive) match, 1 prefix, 2 any other substring
// match. With segments, the last segment of a qualified name counts as the
// name itself ("lodash" is an exact match for "@scope/lodash" and for
// "org.acme:lodash", the way a user types the artifact rather than the
// coordinate); a repository name has no segments. Without a term every row
// ranks 0, leaving the caller's recency tie-break in charge.
//
// The expression binds two arguments starting at placeholder firstArg: the
// escaped term and the escaped term as a prefix pattern, returned as args.
func relevanceExpr(nameExpr, term string, firstArg int, segments bool) (string, []any) {
	if term == "" {
		// Cast: a bare integer in ORDER BY is a column position, not a constant.
		return "0::int", nil
	}

	exact, prefix := fmt.Sprintf("$%d", firstArg), fmt.Sprintf("$%d", firstArg+1)
	escaped := escapeLike(term)
	args := []any{escaped, escaped + "%"}

	exactMatch := nameExpr + " ILIKE " + exact
	prefixMatch := nameExpr + " ILIKE " + prefix

	if segments {
		tail := "substring(" + nameExpr + ` from '[^/:]*$')`
		exactMatch += " OR " + tail + " ILIKE " + exact
		prefixMatch += " OR " + tail + " ILIKE " + prefix
	}

	return "CASE WHEN " + exactMatch + " THEN 0 WHEN " + prefixMatch + " THEN 1 ELSE 2 END", args
}

func whereClause(where string) string {
	if where == "" {
		return ""
	}

	return "WHERE " + where
}

// whereBuilder accumulates AND-ed predicates and their positional arguments,
// so the package and repository queries share the same filter semantics.
type whereBuilder struct {
	clauses []string
	args    []any
}

// add appends a predicate whose single %d is replaced by the placeholder
// number of arg.
func (b *whereBuilder) add(clause string, arg any) {
	b.clauses = append(b.clauses, fmt.Sprintf(clause, len(b.args)+1))
	b.args = append(b.args, arg)
}

// addIn appends "expr IN ($n, ...)" over vals.
func (b *whereBuilder) addIn(expr string, vals []string) {
	placeholders := make([]string, len(vals))

	for i, v := range vals {
		placeholders[i] = fmt.Sprintf("$%d", len(b.args)+1)
		b.args = append(b.args, v)
	}

	b.clauses = append(b.clauses, fmt.Sprintf("%s IN (%s)", expr, strings.Join(placeholders, ",")))
}

// addNameMatch appends the substring predicates for the name: filter and the
// free-text terms against nameExpr. Matching is case-insensitive and literal:
// LIKE metacharacters in the query ('_', '%') never act as wildcards, so
// "ui_kit" does not match "ui-kit".
func (b *whereBuilder) addNameMatch(nameExpr string, filter SearchFilter) {
	for _, term := range []string{filter.Name, filter.FreeText} {
		if term != "" {
			b.add(nameExpr+" ILIKE $%d", "%"+escapeLike(term)+"%")
		}
	}
}

// addReadable restricts repoIDExpr to the caller's readable repositories.
func (b *whereBuilder) addReadable(repoIDExpr string, readableRepos map[uuid.UUID]struct{}) {
	idStrs := make([]string, 0, len(readableRepos))
	for id := range readableRepos {
		idStrs = append(idStrs, id.String())
	}

	b.add(repoIDExpr+" = ANY($%d::uuid[])", "{"+strings.Join(idStrs, ",")+"}")
}

func (b *whereBuilder) String() string {
	return strings.Join(b.clauses, " AND ")
}

// likeEscaper is built once: a Replacer is safe for concurrent use and costs
// kilobytes to construct, which every search would otherwise pay per term.
//
//nolint:gochecknoglobals // immutable, built once
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLike escapes LIKE/ILIKE metacharacters so user input matches literally.
func escapeLike(s string) string {
	return likeEscaper.Replace(s)
}

// buildWhereClause builds the filter predicate for both CountResults and
// Search. readableRepos must be non-empty (callers short-circuit before
// calling this) — its keys are always ANDed in as an explicit repository_id
// scope so a caller can never see rows from a repository they can't read.
func buildWhereClause(filter SearchFilter, readableRepos map[uuid.UUID]struct{}) (string, []any) {
	var b whereBuilder

	b.addNameMatch("s.name", filter)

	if filter.SHA256Prefix != "" {
		b.add("s.sha256 LIKE $%d", escapeLike(filter.SHA256Prefix)+"%")
	}

	if len(filter.Formats) > 0 {
		b.addIn("s.format", filter.Formats)
	}

	if len(filter.Repos) > 0 {
		b.addIn("r.name", filter.Repos)
	}

	b.addReadable("s.repository_id", readableRepos)

	return b.String(), b.args
}
