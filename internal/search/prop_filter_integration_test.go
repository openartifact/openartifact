// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package search

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// propWorld is three raw repositories a, b, c, an npm repository n and a docker
// repository d, with properties stored under the ids of their files and tags (the ids
// the web UI and oa use):
//
//	a/x.txt  status=approved team=core
//	a/y.txt  status=draft client="ACME Corp"
//	b/z.txt  status=approved
//	b/v.txt  none of its own, but repository a stores status=approved under its id
//	c/w.txt  status=approved; c is not readable
//	n/lib    1.0.0 status=approved, 2.0.0 status=draft
//	d:latest status=approved; d:stable status=draft
//	d:old    deleted, status=approved
//	d:edge   none of its own, but repository a stores status=approved under its id
//	d:upper  status=approved stored under the upper-case text of its id (no UUID match)
type propWorld struct {
	db       *sql.DB
	repos    map[string]uuid.UUID
	readable map[uuid.UUID]struct{}
}

func newPropWorld(t *testing.T) *propWorld {
	t.Helper()

	db := newMigratedSearchDB(t)
	exec := func(q string, args ...any) {
		t.Helper()

		if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("%s: %v", strings.Fields(q)[0:3], err)
		}
	}

	repos := map[string]uuid.UUID{}
	for _, r := range []struct{ name, format string }{{"a", "raw"}, {"b", "raw"}, {"c", "raw"}, {"n", "npm"}, {"d", "docker"}} {
		id := uuid.New()
		repos[r.name] = id
		exec(
			`INSERT INTO repositories (id, name, format, project_id) VALUES ($1, $2, $3, '00000000-0000-0000-0000-000000000001')`,
			id,
			r.name,
			r.format,
		)
	}

	file := func(repo, name, version, path string) uuid.UUID {
		t.Helper()

		var pkg, ver uuid.UUID
		row := db.QueryRowContext(context.Background(), `INSERT INTO packages (repository_id, format, name, name_key)
			VALUES ($1, (SELECT format FROM repositories WHERE id = $1), $2, $2)
			ON CONFLICT DO NOTHING RETURNING id`, repos[repo], name)
		if err := row.Scan(&pkg); err != nil {
			if err := db.QueryRowContext(context.Background(), `SELECT id FROM packages WHERE repository_id = $1 AND name = $2`,
				repos[repo], name).Scan(&pkg); err != nil {
				t.Fatal(err)
			}
		}

		if err := db.QueryRowContext(context.Background(), `INSERT INTO package_versions (package_id, repository_id, version, version_key)
			VALUES ($1, $2, $3, $3) RETURNING id`, pkg, repos[repo], version).Scan(&ver); err != nil {
			t.Fatal(err)
		}

		id := uuid.New()
		exec(
			`INSERT INTO package_files (id, version_id, repository_id, path, filename) VALUES ($1, $2, $3, $4, $4)`,
			id,
			ver,
			repos[repo],
			path,
		)

		return id
	}
	dockerTag := func(name string, deleted bool) uuid.UUID {
		t.Helper()

		var id uuid.UUID

		row := db.QueryRowContext(context.Background(), `INSERT INTO docker_tags (repository_id, image, name, manifest_digest, deleted_at)
			VALUES ($1, 'app', $2, 'sha256:'||md5($2), CASE WHEN $3 THEN now() END) RETURNING id`, repos["d"], name, deleted)
		if err := row.Scan(&id); err != nil {
			t.Fatal(err)
		}

		return id
	}
	tagPath := func(repo, path, key, value string) {
		t.Helper()
		exec(
			`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'raw') ON CONFLICT DO NOTHING`,
			repos[repo],
			path,
		)
		exec(`INSERT INTO artifact_properties (repository_id, artifact_ref_id, key, value)
			SELECT $1, id, $3, $4 FROM artifact_refs WHERE repository_id = $1 AND artifact_path = $2`, repos[repo], path, key, value)
	}
	tag := func(repo string, id uuid.UUID, key, value string) {
		t.Helper()
		tagPath(repo, id.String(), key, value)
	}

	x, y := file("a", "x.txt", "", "x.txt"), file("a", "y.txt", "", "y.txt")
	z, v := file("b", "z.txt", "", "z.txt"), file("b", "v.txt", "", "v.txt")
	w := file("c", "w.txt", "", "w.txt")
	lib1, lib2 := file("n", "lib", "1.0.0", "lib-1.0.0.tgz"), file("n", "lib", "2.0.0", "lib-2.0.0.tgz")

	tag("a", x, "status", "approved")
	tag("a", x, "team", "core")
	tag("a", y, "status", "draft")
	tag("a", y, "client", "ACME Corp")
	tag("b", z, "status", "approved")
	tag("a", v, "status", "approved") // stored in a under a file id of b: must not match
	tag("c", w, "status", "approved")
	tag("n", lib1, "status", "approved")
	tag("n", lib2, "status", "draft")

	latest, stable := dockerTag("latest", false), dockerTag("stable", false)
	old, edge, upper := dockerTag("old", true), dockerTag("edge", false), dockerTag("upper", false)
	tag("d", latest, "status", "approved")
	tag("d", stable, "status", "draft")
	tag("d", old, "status", "approved")
	tag("a", edge, "status", "approved") // stored in a under a tag id of d: must not match
	tagPath("d", strings.ToUpper(upper.String()), "status", "approved")
	exec(`ANALYZE`)

	return &propWorld{
		db:       db,
		repos:    repos,
		readable: map[uuid.UUID]struct{}{repos["a"]: {}, repos["b"]: {}, repos["n"]: {}, repos["d"]: {}},
	}
}

// hits runs the search in the given shape and returns repository/name@version, sorted.
func (w *propWorld) hits(t *testing.T, f SearchFilter, byID bool, opts PageOptions) []string {
	t.Helper()

	f.propByID = make([]bool, len(f.Props))
	for i := range f.propByID {
		f.propByID[i] = byID
	}

	results, _, err := NewExecutor(w.db).runSearch(context.Background(), f, 50, 0, w.readable, true, opts)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	out := []string{}
	for _, r := range results {
		out = append(out, r.Repository+"/"+r.Name+"@"+r.Version)
	}

	slices.Sort(out)

	return out
}

// TestSearchByProperty: both shapes (id lookups for few matches, EXISTS for many) select
// the same rows: only readable repositories, only the live file or docker tag the
// property is stored under in its own repository, every filter at once.
func TestSearchByProperty(t *testing.T) {
	w := newPropWorld(t)

	for _, tc := range []struct {
		name string
		f    SearchFilter
		opts PageOptions
		want []string
	}{
		{"q omitted: readable repositories only, own repository only", SearchFilter{Props: []string{"status=approved"}}, PageOptions{},
			[]string{"a/x.txt@", "b/z.txt@", "d/latest@", "n/lib@1.0.0"}},
		{"with text", SearchFilter{FreeText: "x.txt", Props: []string{"status=approved"}}, PageOptions{}, []string{"a/x.txt@"}},
		{"two filters must both hold", SearchFilter{Props: []string{"status=approved", "team=core"}}, PageOptions{}, []string{"a/x.txt@"}},
		{"value with a space", SearchFilter{Props: []string{"client=ACME Corp"}}, PageOptions{}, []string{"a/y.txt@"}},
		{"exact match, no prefix", SearchFilter{Props: []string{"client=ACME"}}, PageOptions{}, []string{}},
		{"no match", SearchFilter{Props: []string{"status=gone"}}, PageOptions{}, []string{}},
		{"with format", SearchFilter{Formats: []string{"npm"}, Props: []string{"status=draft"}}, PageOptions{}, []string{"n/lib@2.0.0"}},
		{"docker tag", SearchFilter{Formats: []string{"docker"}, Props: []string{"status=draft"}}, PageOptions{}, []string{"d/stable@"}},
		{"docker tag with text", SearchFilter{FreeText: "lat", Props: []string{"status=approved"}}, PageOptions{}, []string{"d/latest@"}},
		{"group=package: latest matching version", SearchFilter{Name: "lib", Props: []string{"status=approved"}}, PageOptions{GroupByPackage: true},
			[]string{"n/lib@1.0.0"}},
	} {
		for _, byID := range []bool{true, false} {
			name := tc.name + map[bool]string{true: " (by id)", false: " (exists)"}[byID]
			t.Run(name, func(t *testing.T) {
				if got := w.hits(t, tc.f, byID, tc.opts); !slices.Equal(got, tc.want) {
					t.Errorf("hits %v, want %v", got, tc.want)
				}
			})
		}
	}
}

// TestPlanPropsPicksTheShape: few matching properties are applied by id lookups, many
// by EXISTS, from a bounded count over idx_artifact_properties_key_value.
func TestPlanPropsPicksTheShape(t *testing.T) {
	w := newPropWorld(t)
	ctx := context.Background()

	if _, err := w.db.ExecContext(ctx, `INSERT INTO artifact_properties (repository_id, artifact_ref_id, key, value)
		SELECT repository_id, id, 'bulk', 'yes' FROM artifact_refs, generate_series(1, $1)`, propByIDLimit); err != nil {
		t.Fatal(err)
	}

	f, err := NewExecutor(w.db).planProps(ctx, SearchFilter{Props: []string{"status=approved", "bulk=yes"}})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(f.propByID, []bool{true, false}) {
		t.Errorf("propByID = %v, want [true false]: status=approved is rare, bulk=yes matches more than %d", f.propByID, propByIDLimit)
	}
}

// TestPropFilterPlans: few matches are found through idx_artifact_properties_key_value
// and the files and docker tags probed by primary key. Many matches are applied per
// row (a nested-loop semi-join) to the rows the other filters leave, instead of
// collecting every match first. Sequential scans are disabled so the plan shows the
// indexes each shape can use (TestVulnScanOverviewUsesIndexes does the same).
func TestPropFilterPlans(t *testing.T) {
	w := newPropWorld(t)
	w.fill(t, 2000)

	for _, tc := range []struct {
		name  string
		f     SearchFilter
		byID  bool
		want  []string
		avoid string
	}{
		{"few matches: by id", SearchFilter{Props: []string{"status=approved"}}, true, []string{"idx_artifact_properties_key_value", "package_files_pkey", "docker_tags_pkey"}, ""},
		// Every filler row matches; a selective name leaves a few rows to probe.
		{"many matches with a name: exists", SearchFilter{Name: "f1234", Props: []string{"filler=yes"}}, false,
			[]string{"Nested Loop Semi Join"}, "Hash Semi Join"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			f.propByID = []bool{tc.byID}
			q, args := buildSearchQuery(f, 25, 0, w.readable, true, PageOptions{})
			plan := explainNoSeqScan(t, w.db, q, args...)

			for _, idx := range tc.want {
				if !strings.Contains(plan, idx) {
					t.Errorf("the plan does not use %s:\n%s", idx, plan)
				}
			}

			if tc.avoid != "" && strings.Contains(plan, tc.avoid) {
				t.Errorf("the plan collects every match first (%s):\n%s", tc.avoid, plan)
			}
		})
	}
}

// fill adds n untagged files to repository b, n untagged tags to repository d and n
// properties of another key, so the plans are costed over tables of a realistic shape
// rather than a dozen rows (on which any index order is as cheap as another).
func (w *propWorld) fill(t *testing.T, n int) {
	t.Helper()

	ctx := context.Background()
	b, d := w.repos["b"], w.repos["d"]

	for _, st := range []struct {
		q    string
		args []any
	}{
		{`WITH p AS (INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'raw', 'filler', 'filler') RETURNING id),
			v AS (INSERT INTO package_versions (package_id, repository_id, version, version_key) SELECT id, $1, '', '' FROM p RETURNING id)
		INSERT INTO package_files (version_id, repository_id, path, filename)
			SELECT v.id, $1, 'f' || i, 'f' || i FROM v, generate_series(1, $2) i`, []any{b, n}},
		{`INSERT INTO docker_tags (repository_id, image, name, manifest_digest)
			SELECT $1, 'filler', 't' || i, 'sha256:' || md5(i::text) FROM generate_series(1, $2) i`, []any{d, n}},
		{`INSERT INTO artifact_refs (repository_id, artifact_path, format)
			SELECT repository_id, id::text, 'raw' FROM package_files WHERE repository_id = $1 AND path LIKE 'f%'
			UNION ALL SELECT repository_id, id::text, 'raw' FROM docker_tags WHERE repository_id = $2 AND image = 'filler'
			ON CONFLICT DO NOTHING`, []any{b, d}},
		{`INSERT INTO artifact_properties (repository_id, artifact_ref_id, key, value)
			SELECT repository_id, id, 'filler', 'yes' FROM artifact_refs ON CONFLICT DO NOTHING`, nil},
		{`ANALYZE`, nil},
	} {
		if _, err := w.db.ExecContext(ctx, st.q, st.args...); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
}

func explainNoSeqScan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()

	ctx := context.Background()

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}

	rows, err := conn.QueryContext(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}

		b.WriteString(line + "\n")
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return b.String()
}

// TestSearchByPropertyHasNoRepositoryHits: a property belongs to an artifact, so a
// search with prop returns no repository hits, as with sha256: and flagged.
func TestSearchByPropertyHasNoRepositoryHits(t *testing.T) {
	w := newPropWorld(t)
	e := NewExecutor(w.db)

	all, err := e.SearchRepositories(context.Background(), SearchFilter{Name: "a"}, 10, 0, w.readable)
	if err != nil || len(all) == 0 {
		t.Fatalf("without prop: %v, %v; want repository a", all, err)
	}

	hits, err := e.SearchRepositories(context.Background(), SearchFilter{Name: "a", Props: []string{"status=approved"}}, 10, 0, w.readable)
	if err != nil || len(hits) != 0 {
		t.Errorf("with prop: %v, %v; want none", hits, err)
	}
}
