// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package search

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// aiTagWorld is raw repositories a and c, an npm repository n and a docker
// repository d, with AI analyses stored under the ids of their files and tags:
//
//	a/x.txt   completed, tags Web-Framework, python; summary mentions "zeppelin"
//	a/y.txt   completed, tags python; then re-analysed (processing): keeps its tags
//	a/z.txt   failed first analysis: no tags, summary "python"
//	a/py.txt  no analysis; its name contains "python"
//	c/w.txt   completed, tags python, web-framework; c is not readable
//	n/lib     1.0.0 tagged python, 2.0.0 untagged
//	d:latest  completed, tags python
//	a/v.txt   no analysis of its own, but repository c stores a python analysis under its id
type aiTagWorld struct {
	pg       *database.PostgresDB
	repos    map[string]uuid.UUID
	readable map[uuid.UUID]struct{}
}

func newAITagWorld(t *testing.T) *aiTagWorld {
	t.Helper()

	pg := newMigratedSearchPG(t)
	db := pg.GetDB()
	ctx := context.Background()

	exec := func(q string, args ...any) {
		t.Helper()

		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", strings.Fields(q)[0:3], err)
		}
	}

	repos := map[string]uuid.UUID{}
	for _, r := range []struct{ name, format string }{{"a", "raw"}, {"c", "raw"}, {"n", "npm"}, {"d", "docker"}} {
		id := uuid.New()
		repos[r.name] = id
		exec(`INSERT INTO repositories (id, name, format, project_id) VALUES ($1, $2, $3, '00000000-0000-0000-0000-000000000001')`,
			id, r.name, r.format)
	}

	file := func(repo, name, version, path string) uuid.UUID {
		t.Helper()

		var pkg, ver, id uuid.UUID

		if err := db.QueryRowContext(ctx, `INSERT INTO packages (repository_id, format, name, name_key)
			VALUES ($1, (SELECT format FROM repositories WHERE id = $1), $2, $2)
			ON CONFLICT (repository_id, name_key) DO UPDATE SET name = EXCLUDED.name RETURNING id`, repos[repo], name).Scan(&pkg); err != nil {
			t.Fatal(err)
		}

		if err := db.QueryRowContext(ctx, `INSERT INTO package_versions (package_id, repository_id, version, version_key)
			VALUES ($1, $2, $3, $3) RETURNING id`, pkg, repos[repo], version).Scan(&ver); err != nil {
			t.Fatal(err)
		}

		if err := db.QueryRowContext(ctx, `INSERT INTO package_files (version_id, repository_id, path, filename)
			VALUES ($1, $2, $3, $3) RETURNING id`, ver, repos[repo], path).Scan(&id); err != nil {
			t.Fatal(err)
		}

		return id
	}

	analyse := func(repo string, artifact uuid.UUID, status, summary string, tags ...string) {
		t.Helper()

		if err := pg.UpsertAIAnalysis(ctx, &models.AIAnalysis{
			RepositoryID: repos[repo].String(), ArtifactID: artifact.String(), ArtifactFormat: "raw",
			Status: status, Summary: summary, Tags: tags, LicenseStatus: "UNKNOWN",
		}); err != nil {
			t.Fatal(err)
		}
	}

	x, y, z := file("a", "x.txt", "", "x.txt"), file("a", "y.txt", "", "y.txt"), file("a", "z.txt", "", "z.txt")
	file("a", "python-notes.txt", "", "python-notes.txt")

	v := file("a", "v.txt", "", "v.txt")
	w := file("c", "w.txt", "", "w.txt")
	lib1, lib2 := file("n", "lib", "1.0.0", "lib-1.0.0.tgz"), file("n", "lib", "2.0.0", "lib-2.0.0.tgz")

	var latest uuid.UUID
	if err := db.QueryRowContext(ctx, `INSERT INTO docker_tags (repository_id, image, name, manifest_digest)
		VALUES ($1, 'app', 'latest', 'sha256:'||md5('latest')) RETURNING id`, repos["d"]).Scan(&latest); err != nil {
		t.Fatal(err)
	}

	analyse("a", x, "completed", "a zeppelin of a package", "Web-Framework", "python")
	analyse("a", y, "completed", "", "python")
	analyse("a", y, "processing", "")
	analyse("a", z, "failed", "")
	analyse("c", w, "completed", "", "python", "web-framework")
	analyse("c", v, "completed", "", "python") // stored in c under a file id of a: must not match
	analyse("n", lib1, "completed", "", "python")
	analyse("n", lib2, "completed", "", "rust")
	analyse("d", latest, "completed", "", "python")
	exec(`ANALYZE`)

	return &aiTagWorld{
		pg:       pg,
		repos:    repos,
		readable: map[uuid.UUID]struct{}{repos["a"]: {}, repos["n"]: {}, repos["d"]: {}},
	}
}

func (w *aiTagWorld) hits(t *testing.T, f SearchFilter, opts PageOptions) []string {
	t.Helper()

	results, total, err := NewExecutor(w.pg.GetDB()).SearchPage(context.Background(), f, opts, 50, 0, w.readable)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	out := []string{}
	for _, r := range results {
		out = append(out, r.Repository+"/"+r.Name+"@"+r.Version)
	}

	slices.Sort(out)

	if int(total) != len(out) {
		t.Errorf("total = %d for %d hits %v", total, len(out), out)
	}

	return out
}

// TestSearchByAITag: free text finds the artifacts whose latest completed AI
// analysis carries the text as a tag, whole and in any case, never the summary,
// never a tag of a repository the caller cannot read or of another artifact.
func TestSearchByAITag(t *testing.T) {
	w := newAITagWorld(t)

	for _, tc := range []struct {
		name string
		f    SearchFilter
		opts PageOptions
		want []string
	}{
		{"tag", SearchFilter{FreeText: "python"},
			PageOptions{}, []string{"a/python-notes.txt@", "a/x.txt@", "a/y.txt@", "d/latest@", "n/lib@1.0.0"}},
		{"any case, and a tag written in mixed case", SearchFilter{FreeText: "WEB-framework"}, PageOptions{}, []string{"a/x.txt@"}},
		{"a tag is matched whole, not as a substring", SearchFilter{FreeText: "web"}, PageOptions{}, []string{}},
		{"not as a prefix", SearchFilter{FreeText: "pyth", Formats: []string{"npm"}}, PageOptions{}, []string{}},
		{"the summary is not searchable", SearchFilter{FreeText: "zeppelin"}, PageOptions{}, []string{}},
		{"a failed analysis has no tags", SearchFilter{FreeText: "z.txt"}, PageOptions{}, []string{"a/z.txt@"}},
		{"another tag", SearchFilter{FreeText: "rust"}, PageOptions{}, []string{"n/lib@2.0.0"}},
		{"with format", SearchFilter{FreeText: "python", Formats: []string{"npm"}}, PageOptions{}, []string{"n/lib@1.0.0"}},
		{"with repo", SearchFilter{FreeText: "python", Repos: []string{"a"}}, PageOptions{},
			[]string{"a/python-notes.txt@", "a/x.txt@", "a/y.txt@"}},
		{"docker tag", SearchFilter{FreeText: "python", Formats: []string{"docker"}}, PageOptions{}, []string{"d/latest@"}},
		{"the name: qualifier matches names only", SearchFilter{Name: "python"}, PageOptions{}, []string{"a/python-notes.txt@"}},
		{"group=package", SearchFilter{FreeText: "python", Formats: []string{"npm"}}, PageOptions{GroupByPackage: true},
			[]string{"n/lib@1.0.0"}},
		{"LIKE metacharacters are literal", SearchFilter{FreeText: "py%"}, PageOptions{}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := w.hits(t, tc.f, tc.opts); !slices.Equal(got, tc.want) {
				t.Errorf("hits %v, want %v", got, tc.want)
			}
		})
	}
}

// The tag of a repository the caller cannot read is not found by it, in the
// small-set form of the scope and in the join form of a large one, and is found
// by a caller who can read that repository.
func TestSearchByAITagRespectsReadableRepositories(t *testing.T) {
	w := newAITagWorld(t)
	f := SearchFilter{FreeText: "web-framework"}

	for _, threshold := range []int{readableJoinThreshold, 0} {
		old := readableJoinThreshold
		readableJoinThreshold = threshold

		got := w.hits(t, f, PageOptions{})

		readableJoinThreshold = old

		if !slices.Equal(got, []string{"a/x.txt@"}) {
			t.Errorf("threshold %d: hits %v, want only a/x.txt: c/w.txt is in a repository the caller cannot read", threshold, got)
		}
	}

	w.readable = map[uuid.UUID]struct{}{w.repos["c"]: {}}

	if got := w.hits(t, f, PageOptions{}); !slices.Equal(got, []string{"c/w.txt@"}) {
		t.Errorf("reader of c: hits %v, want c/w.txt", got)
	}

	// c holds an analysis under a file id of a: a reader of both finds the file once,
	// from the analysis of its own repository.
	w.readable = map[uuid.UUID]struct{}{w.repos["a"]: {}, w.repos["c"]: {}}

	if got := w.hits(t, SearchFilter{FreeText: "python", Repos: []string{"a"}}, PageOptions{}); slices.Contains(got, "a/v.txt@") {
		t.Errorf("hits %v: a/v.txt has no analysis in its own repository", got)
	}
}

// The tag lookup runs on idx_ai_results_tags_lower, whose expression is the one
// the search writes.
func TestAITagSearchUsesTheLowerTagIndex(t *testing.T) {
	w := newAITagWorld(t)

	// Enough analyses with other tags that the planner prefers the index to a scan.
	for _, stmt := range []string{
		`INSERT INTO artifact_refs (repository_id, artifact_path, format)
			SELECT $1, 'filler-' || i, 'raw' FROM generate_series(1, 5000) i`,
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type, scanner, status)
			SELECT id, 'ai', '', 'completed' FROM artifact_refs WHERE artifact_path LIKE 'filler-%'`,
		`INSERT INTO ai_results (scan_job_id, tags)
			SELECT sj.id, jsonb_build_array('Tag' || row_number() OVER ()) FROM scan_jobs sj
			JOIN artifact_refs ar ON ar.id = sj.artifact_ref_id WHERE ar.artifact_path LIKE 'filler-%'`,
		`ANALYZE`,
	} {
		var args []any
		if strings.Contains(stmt, "$1") {
			args = []any{w.repos["a"]}
		}

		if _, err := w.pg.GetDB().ExecContext(context.Background(), stmt, args...); err != nil {
			t.Fatal(err)
		}
	}

	q, args := buildSearchQuery(SearchFilter{FreeText: "python"}, 25, 0, w.readable, true, PageOptions{})

	if plan := explainNoSeqScan(t, w.pg.GetDB(), q, args...); !strings.Contains(plan, "idx_ai_results_tags_lower") {
		t.Errorf("the plan does not use idx_ai_results_tags_lower:\n%s", plan)
	}
}
