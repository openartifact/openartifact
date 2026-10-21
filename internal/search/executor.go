// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

// unifiedSearchView returns a subquery joining every format table (through the
// global content-addressed blobs table for sha256/size) into a single shape:
// repository_id, format, name, version, created_at, path, sha256, size,
// group_id, artifact_id_coord, scope. group_id/artifact_id_coord are populated
// only for maven (used to enrich SearchResult.Metadata); scope only for npm.
func unifiedSearchView() string {
	return `(` + strings.Join([]string{
		`SELECT cc.repository_id, 'cargo'::text AS format, cc.name, cc.version, cc.created_at,
			cc.name || '-' || cc.version || '.crate' AS path,
			COALESCE(b.sha256, '') AS sha256, COALESCE(b.size_bytes, 0) AS size,
			NULL::text AS group_id, NULL::text AS artifact_id_coord, NULL::text AS scope
		FROM cargo_crates cc LEFT JOIN blobs b ON b.id = cc.blob_id`,
		`SELECT cp.repository_id, 'conda'::text, cp.name, cp.version, cp.created_at,
			''::text,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM conda_packages cp LEFT JOIN blobs b ON b.id = cp.blob_id`,
		`SELECT co.repository_id, 'conan'::text, co.name, co.version, co.created_at,
			''::text,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM conan_packages co LEFT JOIN blobs b ON b.id = co.blob_id`,
		`SELECT dt.repository_id, 'docker'::text, dt.name, ''::text, dt.created_at,
			''::text,
			COALESCE(regexp_replace(dm.digest, '^sha256:', ''), ''),
			COALESCE(dm.size, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM docker_tags dt
		LEFT JOIN docker_manifests dm
			ON dm.repository_id = dt.repository_id AND dm.image = dt.image AND dm.digest = dt.manifest_digest`,
		`SELECT gm.repository_id, 'gomod'::text, gm.module_path, gm.version, gm.created_at,
			''::text,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM gomod_modules gm LEFT JOIN blobs b ON b.id = gm.zip_blob_id`,
		`SELECT hc.repository_id, 'helm'::text, hc.name, hc.version, hc.created_at,
			hc.name || '-' || hc.version || '.tgz',
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM helm_charts hc LEFT JOIN blobs b ON b.id = hc.blob_id`,
		`SELECT iv.repository_id, 'ivy'::text, iv.organisation || '/' || iv.module, iv.revision, iv.created_at,
			iv.artifact_name || '.' || iv.artifact_ext,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM ivy_artifacts iv LEFT JOIN blobs b ON b.id = iv.blob_id`,
		`SELECT ma.repository_id, 'maven'::text, ma.group_id || ':' || ma.artifact_id_coord, ma.version, ma.created_at,
			ma.repo_path,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			ma.group_id, ma.artifact_id_coord, NULL::text
		FROM maven_artifacts ma LEFT JOIN blobs b ON b.id = ma.blob_id`,
		`SELECT np.repository_id, 'npm'::text, np.full_name, nv.version, nv.created_at,
			nv.tarball_filename,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULLIF(np.scope, '')
		FROM npm_packages np
		JOIN npm_versions nv ON nv.package_id = np.id
		LEFT JOIN blobs b ON b.id = nv.blob_id`,
		`SELECT ng.repository_id, 'nuget'::text, ng.package_id, ng.version, ng.created_at,
			ng.package_id || '.' || ng.version || '.nupkg',
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM nuget_packages ng LEFT JOIN blobs b ON b.id = ng.blob_id`,
		`SELECT pp.repository_id, 'pypi'::text, pp.name, pp.version, pp.created_at,
			pp.filename,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM pypi_packages pp LEFT JOIN blobs b ON b.id = pp.blob_id`,
		`SELECT ra.repository_id, 'raw'::text, ra.path, ''::text, ra.created_at,
			ra.path,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM raw_artifacts ra LEFT JOIN blobs b ON b.id = ra.blob_id`,
		`SELECT rg.repository_id, 'rubygems'::text, rg.name, rg.version, rg.created_at,
			rg.name || '-' || rg.version || '.gem',
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM rubygems_gems rg LEFT JOIN blobs b ON b.id = rg.blob_id`,
		`SELECT sa.repository_id, 'sbt'::text, sa.group_id || '/' || sa.artifact_id, sa.version, sa.created_at,
			sa.filename,
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM sbt_artifacts sa LEFT JOIN blobs b ON b.id = sa.blob_id`,
		`SELECT rp.repository_id, 'rpm'::text, rp.name, rp.version, rp.created_at,
			COALESCE(rp.filename, ''),
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM rpm_packages rp LEFT JOIN blobs b ON b.id = rp.blob_id`,
		`SELECT dp.repository_id, 'deb'::text, dp.name, dp.version, dp.created_at,
			COALESCE(dp.filename, ''),
			COALESCE(b.sha256, ''), COALESCE(b.size_bytes, 0),
			NULL::text, CAST(NULL AS text), NULL::text
		FROM deb_packages dp LEFT JOIN blobs b ON b.id = dp.blob_id`,
	}, "\n\tUNION ALL\n\t") + `)`
}

// CountResults returns how many rows the filter matches, scoped to the
// caller's readable repositories. An empty readableRepos set (no permission
// on anything) always yields 0 — it must never fall back to counting all rows.
func (e *Executor) CountResults(ctx context.Context, filter SearchFilter, readableRepos map[uuid.UUID]struct{}) (int64, error) {
	if len(readableRepos) == 0 {
		return 0, nil
	}

	where, args := buildWhereClause(filter, readableRepos)

	q := `SELECT COUNT(*) FROM ` + unifiedSearchView() + ` AS s
		JOIN repositories r ON r.id = s.repository_id`
	if where != "" {
		q += " WHERE " + where
	}

	var count int64
	if err := e.db.QueryRowContext(ctx, q, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count query: %w", err)
	}

	return count, nil
}

// Search runs the filter scoped to readableRepos. Repo scoping is enforced
// twice: at the SQL level (repository_id = ANY($n)) and again in Go after
// scanning, so a caller with no readable repositories always gets an empty
// result set even if the SQL-level filter were ever bypassed or misapplied.
func (e *Executor) Search(
	ctx context.Context,
	filter SearchFilter,
	limit, offset int,
	readableRepos map[uuid.UUID]struct{},
) ([]SearchResult, error) {
	if len(readableRepos) == 0 {
		return nil, nil
	}

	where, args := buildWhereClause(filter, readableRepos)

	orderBy := "s.created_at DESC"

	limitN := len(args) + 1
	offsetN := len(args) + 2
	args = append(args, limit, offset)

	q := fmt.Sprintf(`SELECT
		s.repository_id,
		r.name AS repository,
		s.format,
		s.name,
		s.version,
		s.path,
		s.sha256,
		s.size,
		s.created_at,
		s.group_id,
		s.artifact_id_coord,
		s.scope
	FROM `+unifiedSearchView()+` AS s
	JOIN repositories r ON r.id = s.repository_id
	%s
	ORDER BY %s
	LIMIT $%d OFFSET $%d`,
		whereClause(where),
		orderBy,
		limitN,
		offsetN,
	)

	rows, err := e.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []SearchResult

	for rows.Next() {
		var (
			repoIDRaw  string
			sr         SearchResult
			groupID    sql.NullString
			artifactID sql.NullString
			scope      sql.NullString
		)

		if err := rows.Scan(&repoIDRaw, &sr.Repository, &sr.Format, &sr.Name, &sr.Version,
			&sr.Path, &sr.SHA256, &sr.Size, &sr.CreatedAt, &groupID, &artifactID, &scope); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
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

	return results, rows.Err()
}

// SearchRepositories returns the repositories themselves whose name matches the
// filter (case-insensitive substring, so "ui-kit" finds "ui-kit", "my-ui-kit-lib"
// and "ui-kit-docs"), ordered by name. The package-oriented terms of the filter
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

	limitN := len(b.args) + 1
	b.args = append(b.args, limit, offset)

	//nolint:gosec // G201: the predicate is built from constant fragments and $n placeholders only; every user value is a bound argument
	q := fmt.Sprintf(`SELECT r.id, r.name, r.format, COALESCE(r.description, '')
	FROM repositories r
	WHERE %s
	ORDER BY r.name, r.id
	LIMIT $%d OFFSET $%d`, b.String(), limitN, limitN+1)

	//nolint:gosec // G701: see G201 above, q contains placeholders only
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

// escapeLike escapes LIKE/ILIKE metacharacters so user input matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
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
