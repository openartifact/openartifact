// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/search"
)

//go:embed testdata/search_legacy_view.sql
var legacySearchView string

// searchEquivalenceSeed puts rows in every searchable format table. created_at
// is unique per row (a shared sequence) because the legacy query orders by
// created_at alone, so ties would make its page order arbitrary. Some rows have
// no blob, and docker tags either have or lack a manifest.
const searchEquivalenceSeed = `
CREATE SEQUENCE IF NOT EXISTS eq_seq;
INSERT INTO blobs (sha256, size_bytes, blob_key)
SELECT md5(g::text) || md5((g + 1)::text), 100 + g, 'k' || g FROM generate_series(1, 20) g ON CONFLICT DO NOTHING;
DROP TABLE IF EXISTS eq_blob, eq_ts;
CREATE TEMP TABLE eq_blob AS SELECT row_number() OVER (ORDER BY size_bytes) AS n, id, sha256 FROM blobs;
CREATE TEMP TABLE eq_ts AS SELECT g, ((g % 20) + 1) AS bn FROM generate_series(1, 30) g;

INSERT INTO deb_packages (repository_id, distribution_id, component_id, blob_id, name, version, arch, filename, created_at, updated_at)
SELECT $1, $2, $3, b.id, 'deb' || g, '1.' || g, 'amd64', 'pool/' || g || '.deb', now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO rpm_packages (repository_id, blob_id, name, version, release, arch, filename, created_at, updated_at)
SELECT $1, b.id, 'rpm' || g, '2.' || g, '1', 'x86_64', g || '.rpm', now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO npm_packages (id, repository_id, name, full_name, scope)
SELECT md5($1::text || g)::uuid, $1, 'p' || g, '@s/p' || g, '@s' FROM generate_series(1, 5) g;
INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id, created_at, updated_at)
SELECT md5($1::text || (1 + g % 5))::uuid, '3.' || g, '{}'::jsonb, 'i', 's', 'p-' || g || '.tgz', b.id,
       now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO pypi_packages (repository_id, name, version, filename, package_type, python_version, blob_id, created_at, updated_at)
SELECT $1, 'py' || g, '4.' || g, 'py' || g || '.whl', 'bdist_wheel', 'py3', b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id, created_at, updated_at)
SELECT $1, 'org.g' || (g % 3), 'art' || g, '5.' || g, 'jar', 'org/art' || g || '.jar', b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO ivy_artifacts (repository_id, organisation, module, revision, artifact_name, artifact_ext, blob_id, created_at, updated_at)
SELECT $1, 'org', 'mod' || g, '6.' || g, 'mod' || g, 'jar', b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO sbt_artifacts (repository_id, group_id, artifact_id, version, scala_version, filename, blob_id, created_at, updated_at)
SELECT $1, 'org', 'lib' || g, '7.' || g, '2.13', 'lib' || g || '.jar', b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO nuget_packages (repository_id, package_id, version, blob_id, created_at, updated_at)
SELECT $1, 'Nu' || g, '8.' || g, b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now() FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id, mod_blob_id, created_at, updated_at)
SELECT $1, 'example.com/m' || g, 'v9.' || g, 'module m', CASE WHEN g % 2 = 0 THEN b.id END, b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO cargo_crates (repository_id, name, version, blob_id, created_at, updated_at)
SELECT $1, 'crt' || g, '10.' || g, b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now() FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO rubygems_gems (repository_id, name, version, platform, blob_id, created_at, updated_at)
SELECT $1, 'gem' || g, '11.' || g, 'ruby', b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now() FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO conda_packages (repository_id, name, version, build_string, platform, blob_id, created_at, updated_at)
SELECT $1, 'cnd' || g, '12.' || g, 'h' || g, 'linux-64', b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now() FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO conan_packages (repository_id, name, version, blob_id, created_at, updated_at)
SELECT $1, 'cn' || g, '13.' || g, CASE WHEN g % 2 = 0 THEN b.id END, now() - (nextval('eq_seq') || ' seconds')::interval, now()
FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO helm_charts (repository_id, name, version, blob_id, created_at, updated_at)
SELECT $1, 'ch' || g, '14.' || g, b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now() FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO raw_artifacts (repository_id, path, blob_id, created_at, updated_at)
SELECT $1, 'dir/f' || g, b.id, now() - (nextval('eq_seq') || ' seconds')::interval, now() FROM eq_ts JOIN eq_blob b ON b.n = bn;
INSERT INTO docker_manifests (repository_id, digest, media_type, size, blob_id)
SELECT $1, 'sha256:' || md5(g::text), 'm', 900 + g, b.id FROM eq_ts JOIN eq_blob b ON b.n = bn WHERE g % 2 = 0;
INSERT INTO docker_tags (repository_id, name, manifest_digest, created_at, updated_at)
SELECT $1, 't' || g, 'sha256:' || md5(g::text), now() - (nextval('eq_seq') || ' seconds')::interval, now() FROM eq_ts;
`

// seedSearchEquivalence seeds the search fixture into a repository named
// repoName and returns its id.
func seedSearchEquivalence(t *testing.T, pg *database.PostgresDB, repoName string) string {
	t.Helper()

	ctx := context.Background()
	repo := newRepo(t, pg, repoName, "raw")

	var distID, compID string
	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO debian_distributions (repository_id, name) VALUES ($1, 'stable') RETURNING id`, repo).Scan(&distID); err != nil {
		t.Fatalf("insert debian_distributions: %v", err)
	}

	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO debian_components (distribution_id, name) VALUES ($1, 'main') RETURNING id`, distID).Scan(&compID); err != nil {
		t.Fatalf("insert debian_components: %v", err)
	}

	conn, err := pg.GetDB().Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}

	defer func() { _ = conn.Close() }()

	script := strings.NewReplacer("$1", "'"+repo+"'", "$2", "'"+distID+"'", "$3", "'"+compID+"'").Replace(searchEquivalenceSeed)
	if _, err := conn.ExecContext(ctx, script); err != nil {
		t.Fatalf("seed: %v", err)
	}

	return repo
}

type searchEquivalenceCase struct {
	name        string
	filter      search.SearchFilter
	legacyWhere string // predicate on the legacy view s / repositories r, placeholders from $1
	legacyArgs  []any
}

func searchEquivalenceCases() []searchEquivalenceCase {
	return []searchEquivalenceCase{
		{"free text", search.SearchFilter{FreeText: "art1"}, `s.name ILIKE $1`, []any{"%art1%"}},
		{"broad text", search.SearchFilter{FreeText: "p"}, `s.name ILIKE $1`, []any{"%p%"}},
		{"name filter", search.SearchFilter{Name: "org.g1"}, `s.name ILIKE $1`, []any{"%org.g1%"}},
		{"format", search.SearchFilter{Formats: []string{"docker"}}, `s.format IN ($1)`, []any{"docker"}},
		{"two formats and text", search.SearchFilter{Formats: []string{"maven", "npm"}, FreeText: "1"}, `s.name ILIKE $1 AND s.format IN ($2,$3)`, []any{"%1%", "maven", "npm"}},
		{"repo", search.SearchFilter{Repos: []string{"search-eq"}, FreeText: "crt"}, `s.name ILIKE $1 AND r.name IN ($2)`, []any{"%crt%", "search-eq"}},
		{"sha256 prefix", search.SearchFilter{SHA256Prefix: "c4ca"}, `s.sha256 LIKE $1`, []any{"c4ca%"}},
		{"sha256 docker digest", search.SearchFilter{SHA256Prefix: "c81e", Formats: []string{"docker"}}, `s.sha256 LIKE $1 AND s.format IN ($2)`, []any{"c81e%", "docker"}},
		{"no match", search.SearchFilter{FreeText: "zzz-nothing"}, `s.name ILIKE $1`, []any{"%zzz-nothing%"}},
	}
}

func legacySearch(t *testing.T, pg *database.PostgresDB, c searchEquivalenceCase, readable []string, limit, offset int) []search.SearchResult {
	t.Helper()

	args := append([]any{}, c.legacyArgs...)

	// The legacy view predates relevance ranking: rank it independently of
	// the production expression (exact name, prefix, other; the last
	// "/" or ":" segment of a qualified name counts as the name).
	term := c.filter.Name
	if term == "" {
		term = c.filter.FreeText
	}

	rank := "0::int" // a bare 0 would be an ORDER BY column position

	if term != "" {
		args = append(args, term)
		rank = fmt.Sprintf(`CASE
			WHEN lower(s.name) = lower($%[1]d) OR lower(substring(s.name from '[^/:]*$')) = lower($%[1]d) THEN 0
			WHEN starts_with(lower(s.name), lower($%[1]d)) OR starts_with(lower(substring(s.name from '[^/:]*$')), lower($%[1]d)) THEN 1
			ELSE 2 END`, len(args))
	}

	n := len(args)
	args = append(args, "{"+strings.Join(readable, ",")+"}", limit, offset)

	q := fmt.Sprintf(`SELECT s.repository_id, r.name, s.format, s.name, s.version, s.path, s.sha256, s.size, s.created_at, s.group_id, s.artifact_id_coord, s.scope
		FROM %s AS s JOIN repositories r ON r.id = s.repository_id
		WHERE %s AND s.repository_id = ANY($%d::uuid[])
		ORDER BY %s, s.created_at DESC LIMIT $%d OFFSET $%d`, legacySearchView, c.legacyWhere, n+1, rank, n+2, n+3)

	rows, err := pg.GetDB().QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("legacy search: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var out []search.SearchResult

	for rows.Next() {
		var (
			repoID, groupID, artifactID, scope = "", (*string)(nil), (*string)(nil), (*string)(nil)
			sr                                 search.SearchResult
		)

		if err := rows.Scan(&repoID, &sr.Repository, &sr.Format, &sr.Name, &sr.Version, &sr.Path, &sr.SHA256, &sr.Size, &sr.CreatedAt, &groupID, &artifactID, &scope); err != nil {
			t.Fatalf("scan: %v", err)
		}

		sr.Metadata = map[string]string{}
		for k, v := range map[string]*string{"groupId": groupID, "artifactId": artifactID, "scope": scope} {
			if v != nil {
				sr.Metadata[k] = *v
			}
		}

		out = append(out, sr)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	return out
}

func legacySearchCount(t *testing.T, pg *database.PostgresDB, c searchEquivalenceCase, readable []string) int64 {
	t.Helper()

	n := len(c.legacyArgs)
	args := append(append([]any{}, c.legacyArgs...), "{"+strings.Join(readable, ",")+"}")

	q := fmt.Sprintf(`SELECT COUNT(*) FROM %s AS s JOIN repositories r ON r.id = s.repository_id
		WHERE %s AND s.repository_id = ANY($%d::uuid[])`, legacySearchView, c.legacyWhere, n+1)

	var count int64
	if err := pg.GetDB().QueryRowContext(context.Background(), q, args...).Scan(&count); err != nil {
		t.Fatalf("legacy count: %v", err)
	}

	return count
}

// TestSearchMatchesLegacyQuery: the staged search (narrow union, page first,
// blobs/repositories joined for the page only) must return the legacy view's
// rows, fields, order and total for every filter kind and page, across two
// repositories and with the readable set restricted to one of them.
func TestSearchMatchesLegacyQuery(t *testing.T) {
	pg := freshPostgres(t, 5)
	repoA := seedSearchEquivalence(t, pg, "search-eq")
	repoB := seedSearchEquivalence(t, pg, "search-eq-b")
	exec := search.NewExecutor(pg.GetDB())

	scopes := map[string][]string{"both": {repoA, repoB}, "only A": {repoA}}

	for scopeName, ids := range scopes {
		readable := map[uuid.UUID]struct{}{}
		for _, id := range ids {
			readable[uuid.MustParse(id)] = struct{}{}
		}

		for _, c := range searchEquivalenceCases() {
			for _, page := range []struct{ limit, offset int }{{5, 0}, {5, 5}, {25, 0}, {1000, 0}, {7, 60}} {
				t.Run(fmt.Sprintf("%s/%s/limit=%d/offset=%d", scopeName, c.name, page.limit, page.offset), func(t *testing.T) {
					want := legacySearch(t, pg, c, ids, page.limit, page.offset)
					if page.offset == 0 && len(want) == 0 && c.name != "no match" {
						t.Fatal("legacy query returned no rows: the seed is not exercising this filter")
					}

					got, err := exec.Search(context.Background(), c.filter, page.limit, page.offset, readable)
					if err != nil {
						t.Fatalf("Search: %v", err)
					}

					if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
						t.Fatalf("page differs:\n got  %+v\n want %+v", got, want)
					}

					wantCount := legacySearchCount(t, pg, c, ids)

					// SearchPage: same page and the total from one scan, also
					// for pages past the end.
					gotPage, gotTotal, err := exec.SearchPage(context.Background(), c.filter, search.PageOptions{}, page.limit, page.offset, readable)
					if err != nil {
						t.Fatalf("SearchPage: %v", err)
					}

					if fmt.Sprintf("%+v", gotPage) != fmt.Sprintf("%+v", want) {
						t.Fatalf("SearchPage page differs:\n got  %+v\n want %+v", gotPage, want)
					}

					if gotTotal != wantCount {
						t.Fatalf("SearchPage total = %d, legacy %d", gotTotal, wantCount)
					}

					gotCount, err := exec.CountResults(context.Background(), c.filter, readable)
					if err != nil {
						t.Fatalf("CountResults: %v", err)
					}

					if gotCount != wantCount {
						t.Fatalf("count = %d, legacy %d", gotCount, wantCount)
					}
				})
			}
		}
	}
}
