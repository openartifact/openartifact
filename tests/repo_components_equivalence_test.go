// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

//go:embed testdata/list_repo_components_legacy.sql
var legacyListRepoComponents string

// equivalenceSeed fills one repository with rows in every per-format table.
// created_at is truncated to a few distinct minutes so the (created_at DESC, id)
// tiebreak is exercised; some rows have no blob, no manifest, a zero rubygems
// size and a NULL rpm package_size to cover the per-format size rules.
const equivalenceSeed = `
INSERT INTO blobs (sha256, size_bytes, blob_key)
SELECT md5(g::text) || md5((g + 1)::text), 100 + g, 'k' || g FROM generate_series(1, 30) g;

CREATE TEMP TABLE eq_blob AS
SELECT row_number() OVER (ORDER BY size_bytes) AS n, id, sha256 FROM blobs;

CREATE TEMP TABLE eq_ts AS
SELECT g, date_trunc('minute', now()) - ((g % 5) || ' minutes')::interval AS ts FROM generate_series(1, 40) g;

INSERT INTO deb_packages (repository_id, distribution_id, component_id, blob_id, name, version, arch, filename, installed_size, created_at, updated_at)
SELECT $1, $2, $3, b.id, 'deb' || g, '1.' || g, 'amd64', 'pool/' || g || '.deb', g, ts, ts AS ts_updated
FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO rpm_packages (repository_id, blob_id, name, version, release, arch, filename, package_size, installed_size, archive_size, build_time, created_at, updated_at)
SELECT $1, b.id, 'rpm' || g, '2.' || g, '1', 'x86_64', g || '.rpm', CASE WHEN g % 2 = 0 THEN NULL ELSE 7000 + g END, g, 3 * g, ts, ts AS ts_updated, ts AS ts_created
FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO npm_packages (id, repository_id, name, full_name)
SELECT ('00000000-0000-0000-0000-' || lpad(g::text, 12, '0'))::uuid, $1, 'p' || g, '@s/p' || g FROM generate_series(1, 10) g;
INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id, created_at, updated_at)
SELECT ('00000000-0000-0000-0000-' || lpad((1 + g % 10)::text, 12, '0'))::uuid, '3.' || g, '{}'::jsonb, 'i', 's', 'p-' || g || '.tgz', b.id, ts, ts AS ts_updated
FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO pypi_packages (repository_id, name, version, filename, package_type, python_version, blob_id, created_at, updated_at)
SELECT $1, 'py' || g, '4.' || g, 'py' || g || '.whl', 'bdist_wheel', 'py3', b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, classifier, extension, repo_path, blob_id, created_at, updated_at)
SELECT $1, 'org.g' || (g % 3), 'art' || g, '5.' || g, CASE WHEN g % 2 = 0 THEN 'sources' ELSE '' END, 'jar', 'org/art' || g || '.jar', b.id, ts, ts AS ts_updated
FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO ivy_artifacts (repository_id, organisation, module, revision, artifact_name, artifact_ext, blob_id, created_at, updated_at)
SELECT $1, 'org', 'mod' || g, '6.' || g, 'mod' || g, 'jar', b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO sbt_artifacts (repository_id, group_id, artifact_id, version, scala_version, filename, blob_id, created_at, updated_at)
SELECT $1, 'org', 'lib' || g, '7.' || g, '2.13', 'lib' || g || '.jar', b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO nuget_packages (repository_id, package_id, version, blob_id, created_at, updated_at)
SELECT $1, 'Nu' || g, '8.' || g, b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id, mod_blob_id, created_at, updated_at)
SELECT $1, 'example.com/m' || g, 'v9.' || g, 'module m', CASE WHEN g % 2 = 0 THEN b.id END, b.id, ts, ts AS ts_updated
FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO cargo_crates (repository_id, name, version, blob_id, created_at, updated_at)
SELECT $1, 'crt' || g, '10.' || g, b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO rubygems_gems (repository_id, name, version, platform, blob_id, size, created_at, updated_at)
SELECT $1, 'gem' || g, '11.' || g, CASE WHEN g % 2 = 0 THEN 'java' ELSE 'ruby' END, b.id, CASE WHEN g % 3 = 0 THEN 0 ELSE 5000 + g END, ts, ts AS ts_updated
FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO conda_packages (repository_id, name, version, build_string, platform, blob_id, created_at, updated_at)
SELECT $1, 'cnd' || g, '12.' || g, 'h' || g, 'linux-64', b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO conan_packages (repository_id, name, version, blob_id, created_at, updated_at)
SELECT $1, 'cn' || g, '13.' || g, CASE WHEN g % 2 = 0 THEN b.id END, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO helm_charts (repository_id, name, version, blob_id, created_at, updated_at)
SELECT $1, 'ch' || g, '14.' || g, b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO raw_artifacts (repository_id, path, blob_id, created_at, updated_at)
SELECT $1, 'dir/f' || g, b.id, ts, ts AS ts_updated FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO proxy_cache_entries (repository_id, cache_key, blob_key, size_bytes, status_code, expires_at, created_at, updated_at)
SELECT $1, 'up/' || g, CASE WHEN g % 4 = 0 THEN NULL WHEN g % 4 = 1 THEN 'no-such-blob' ELSE b.sha256 END,
       777 + g, CASE WHEN g % 5 = 0 THEN 404 ELSE 200 END, now() + interval '1 hour', ts, ts AS ts_updated
FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30;

INSERT INTO docker_manifests (repository_id, digest, media_type, size, blob_id)
SELECT $1, 'sha256:' || md5(g::text), 'm', 900 + g, b.id FROM eq_ts JOIN eq_blob b ON b.n = 1 + g % 30 WHERE g % 2 = 0;
INSERT INTO docker_tags (repository_id, name, manifest_digest, created_at, updated_at)
SELECT $1, 't' || g, 'sha256:' || md5(g::text), ts, ts AS ts_updated FROM eq_ts;
`

func seedEquivalenceRepo(t *testing.T, pg *database.PostgresDB) string {
	t.Helper()

	ctx := context.Background()
	repo := newRepo(t, pg, "equivalence", "raw")

	var distID, compID string
	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO debian_distributions (repository_id, name) VALUES ($1, 'stable') RETURNING id`, repo).Scan(&distID); err != nil {
		t.Fatalf("insert debian_distributions: %v", err)
	}

	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO debian_components (distribution_id, name) VALUES ($1, 'main') RETURNING id`, distID).Scan(&compID); err != nil {
		t.Fatalf("insert debian_components: %v", err)
	}

	// The seed is one multi-statement script (simple protocol, no bind
	// parameters): the three ids are substituted through a session table.
	conn, err := pg.GetDB().Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}

	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf(
		`CREATE TEMP TABLE eq_ids AS SELECT '%s'::uuid AS repo, '%s'::uuid AS dist, '%s'::uuid AS comp`, repo, distID, compID)); err != nil {
		t.Fatalf("create eq_ids: %v", err)
	}

	script := strings.NewReplacer(
		"$1", "(SELECT repo FROM eq_ids)",
		"$2", "(SELECT dist FROM eq_ids)",
		"$3", "(SELECT comp FROM eq_ids)",
	).Replace(equivalenceSeed)

	if _, err := conn.ExecContext(ctx, script); err != nil {
		t.Fatalf("seed: %v", err)
	}

	return repo
}

// legacyList runs the pre-rewrite query and maps it onto models.Component.
func legacyList(t *testing.T, pg *database.PostgresDB, repoID, format string, page, limit int) []*models.Component {
	t.Helper()

	rows, err := pg.GetDB().QueryContext(context.Background(), legacyListRepoComponents, repoID, format, (page-1)*limit, limit)
	if err != nil {
		t.Fatalf("legacy query: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var out []*models.Component

	for rows.Next() {
		c := &models.Component{}
		if err := rows.Scan(&c.ID, &c.RepositoryID, &c.Name, &c.Version, &c.Format, &c.Arch, &c.SHA256, &c.Release, &c.Epoch,
			&c.BuildTime, &c.ArchiveSize, &c.PackageSize, &c.InstalledSize, &c.Filename, &c.CreatedAt, &c.UpdatedAt); err != nil {
			t.Fatalf("scan legacy row: %v", err)
		}

		out = append(out, c)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	return out
}

// TestListRepoComponentsMatchesLegacyQuery: the two-stage listing must return
// exactly the rows, fields and order of the pre-rewrite query for every format
// filter and page.
func TestListRepoComponentsMatchesLegacyQuery(t *testing.T) {
	pg := freshPostgres(t, 5)
	repo := seedEquivalenceRepo(t, pg)

	for _, format := range []string{"", "maven", "rpm", "rubygems", "conan", "docker", "raw", "npm", "go", "deb"} {
		for _, limit := range []int{1, 7, 25, 1000} {
			for page := 1; page <= 4; page++ {
				name := fmt.Sprintf("format=%q/limit=%d/page=%d", format, limit, page)
				t.Run(name, func(t *testing.T) {
					want := legacyList(t, pg, repo, format, page, limit)
					if page == 1 && len(want) == 0 {
						t.Fatal("legacy query returned no rows: the seed is not exercising this format")
					}

					got, _, err := pg.ListComponentsByRepo(context.Background(), repo, format, page, limit)
					if err != nil {
						t.Fatalf("ListComponentsByRepo: %v", err)
					}

					assertSameComponents(t, want, got)
				})
			}
		}
	}
}

func assertSameComponents(t *testing.T, want, got []*models.Component) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d rows, legacy returned %d", len(got), len(want))
	}

	for i := range want {
		if fmt.Sprintf("%+v", *got[i]) != fmt.Sprintf("%+v", *want[i]) {
			t.Fatalf("row %d differs:\n got  %+v\n want %+v", i, *got[i], *want[i])
		}
	}
}

func componentIDs(list []*models.Component) []string {
	ids := make([]string, len(list))
	for i, c := range list {
		ids[i] = c.ID
	}

	return ids
}

// TestSearchComponentsQueryMatchesEveryFormat: q keeps exactly the rows whose
// listed name or version contains the text, case-insensitively, in every
// format branch, and the total counts the same rows.
func TestSearchComponentsQueryMatchesEveryFormat(t *testing.T) {
	pg := freshPostgres(t, 5)
	repo := seedEquivalenceRepo(t, pg)
	ctx := context.Background()

	all, _, err := pg.SearchComponents(ctx, repo, database.ComponentListOptions{Page: 1, Limit: 10000})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}

	for _, q := range []string{"1", "Ab", "EXAMPLE.com", "dir/f1", "equivalence", "t3", "no-such-text", "v9.", "org.g1"} {
		t.Run(q, func(t *testing.T) {
			var want []string

			for _, c := range all {
				lq := strings.ToLower(q)
				if strings.Contains(strings.ToLower(c.Name), lq) || strings.Contains(strings.ToLower(c.Version), lq) {
					want = append(want, c.ID)
				}
			}

			got, total, err := pg.SearchComponents(ctx, repo, database.ComponentListOptions{Query: q, Page: 1, Limit: 10000})
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			if total != len(want) {
				t.Fatalf("total = %d, want %d", total, len(want))
			}

			if fmt.Sprint(componentIDs(got)) != fmt.Sprint(want) {
				t.Fatalf("rows differ from the newest-first filter of the full list:\n got  %v\n want %v", componentIDs(got), want)
			}
		})
	}

	// "%" and "_" are literal characters, not LIKE wildcards.
	if got, total, err := pg.SearchComponents(ctx, repo, database.ComponentListOptions{Query: "%", Page: 1, Limit: 10}); err != nil || total != 0 || len(got) != 0 {
		t.Fatalf("q=%%: total=%d len=%d err=%v, want no match", total, len(got), err)
	}
}

// TestSearchComponentsSortByName: the by-name order is a total order over
// every format (same rows as newest-first, no row lost or repeated across
// pages) and sorted within a format.
func TestSearchComponentsSortByName(t *testing.T) {
	pg := freshPostgres(t, 5)
	repo := seedEquivalenceRepo(t, pg)
	ctx := context.Background()

	newest, newestTotal, err := pg.SearchComponents(ctx, repo, database.ComponentListOptions{Page: 1, Limit: 10000})
	if err != nil {
		t.Fatalf("list newest: %v", err)
	}

	for _, format := range []string{"", "maven", "rpm", "docker", "raw", "npm", "go", "deb"} {
		t.Run("pages/"+format, func(t *testing.T) {
			base := database.ComponentListOptions{Format: format, SortByName: true}

			one := base
			one.Page, one.Limit = 1, 10000

			whole, total, err := pg.SearchComponents(ctx, repo, one)
			if err != nil {
				t.Fatalf("by name: %v", err)
			}

			if format == "" && (total != newestTotal || len(whole) != len(newest)) {
				t.Fatalf("by-name total/len = %d/%d, newest-first %d/%d", total, len(whole), newestTotal, len(newest))
			}

			var paged []*models.Component

			for page := 1; page <= (total+6)/7; page++ {
				opts := base
				opts.Page, opts.Limit = page, 7

				rows, pageTotal, err := pg.SearchComponents(ctx, repo, opts)
				if err != nil || pageTotal != total {
					t.Fatalf("page %d: total=%d err=%v, want total %d", page, pageTotal, err, total)
				}

				paged = append(paged, rows...)
			}

			if fmt.Sprint(componentIDs(paged)) != fmt.Sprint(componentIDs(whole)) {
				t.Fatalf("pages of 7 do not add up to the whole listing:\n got  %v\n want %v", componentIDs(paged), componentIDs(whole))
			}
		})
	}

	// Within one format whose names are prefix+number, the order is name then version.
	for _, format := range []string{"deb", "pypi", "cargo", "helm"} {
		rows, _, err := pg.SearchComponents(ctx, repo, database.ComponentListOptions{Format: format, SortByName: true, Page: 1, Limit: 100})
		if err != nil || len(rows) == 0 {
			t.Fatalf("%s by name: len=%d err=%v", format, len(rows), err)
		}

		names := make([]string, len(rows))
		for i, c := range rows {
			names[i] = c.Name
		}

		if !sort.StringsAreSorted(names) {
			t.Fatalf("%s names not sorted: %v", format, names)
		}
	}

	// The two orders can be combined with q.
	rows, total, err := pg.SearchComponents(ctx, repo, database.ComponentListOptions{Query: "py1", Format: "pypi", SortByName: true, Page: 1, Limit: 100})
	if err != nil || total != len(rows) || len(rows) == 0 || rows[0].Name != "py1" {
		t.Fatalf("q+sort: total=%d len=%d err=%v first=%v", total, len(rows), err, rows)
	}
}
