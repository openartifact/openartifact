// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package search

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newMigratedSearchDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	dbName := fmt.Sprintf("oa_search_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: dbName, SSLMode: sslmode, MaxOpenConns: 4, MaxIdleConns: 4,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return pg.GetDB()
}

const scopeTestRepos = 1300

// seedScopeData inserts scopeTestRepos repositories of three formats (npm, raw
// and helm, which holds nothing), every 17th of them soft-deleted, with
// packages and files whose names repeat across repositories so that filters
// match many of them. It returns every repository id, deleted ones included.
func seedScopeData(t *testing.T, db *sql.DB) []uuid.UUID {
	t.Helper()

	ctx := context.Background()

	stmts := []string{
		`INSERT INTO blobs (id, sha256, size_bytes, blob_key)
		 VALUES ('00000000-0000-0000-0000-0000000000b1', repeat('a', 64), 7, 'scope-test')`,
		fmt.Sprintf(`INSERT INTO repositories (name, format, project_id, deleted_at)
		 SELECT 'repo-' || i,
		        CASE i %% 3 WHEN 0 THEN 'npm' WHEN 1 THEN 'raw' ELSE 'helm' END,
		        '00000000-0000-0000-0000-000000000001',
		        CASE WHEN i %% 17 = 0 THEN now() END
		 FROM generate_series(1, %d) AS i`, scopeTestRepos),
		`INSERT INTO npm_packages (repository_id, name, full_name)
		 SELECT r.id, n.name, n.name
		 FROM repositories r
		 CROSS JOIN LATERAL (VALUES ('lib-' || (substr(r.name, 6)::int % 5)), ('pkg-' || substr(r.name, 6))) AS n(name)
		 WHERE r.format = 'npm'`,
		`INSERT INTO npm_versions (package_id, version, manifest_json, integrity, shasum, tarball_filename, blob_id, created_at)
		 SELECT np.id, v.version, '{}', 'x', 'x', np.name || '-' || v.version || '.tgz',
		        '00000000-0000-0000-0000-0000000000b1',
		        now() - make_interval(secs => substr(r.name, 6)::int * 10 + v.delta)
		 FROM npm_packages np
		 JOIN repositories r ON r.id = np.repository_id
		 CROSS JOIN (VALUES ('1.0.0', 1), ('1.1.0', 2)) AS v(version, delta)`,
		`INSERT INTO raw_artifacts (repository_id, path, blob_id, created_at)
		 SELECT r.id, f.path, '00000000-0000-0000-0000-0000000000b1',
		        now() - make_interval(secs => substr(r.name, 6)::int * 10 + f.delta)
		 FROM repositories r
		 CROSS JOIN LATERAL (VALUES ('dir/lib-' || (substr(r.name, 6)::int % 5) || '.bin', 3), ('misc/' || substr(r.name, 6), 4)) AS f(path, delta)
		 WHERE r.format = 'raw'`,
	}

	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed: %v\n%s", err, s)
		}
	}

	rows, err := db.QueryContext(ctx, `SELECT id FROM repositories ORDER BY name`)
	if err != nil {
		t.Fatalf("list repositories: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []uuid.UUID

	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("list repositories: %v", err)
	}

	if len(ids) != scopeTestRepos {
		t.Fatalf("seeded %d repositories, want %d", len(ids), scopeTestRepos)
	}

	return ids
}

// principalSets returns the readable sets of principals of every shape: no
// access, a few repositories, a project's worth, everything, a set straddling
// the join threshold, and sets holding ids of unknown or deleted repositories.
func principalSets(all []uuid.UUID) map[string]map[uuid.UUID]struct{} {
	pick := func(keep func(i int) bool) map[uuid.UUID]struct{} {
		set := map[uuid.UUID]struct{}{}

		for i, id := range all {
			if keep(i) {
				set[id] = struct{}{}
			}
		}

		return set
	}

	rng := rand.New(rand.NewPCG(1, 2))
	random := func(n int) map[uuid.UUID]struct{} {
		set := map[uuid.UUID]struct{}{}
		for _, i := range rng.Perm(len(all))[:n] {
			set[all[i]] = struct{}{}
		}

		return set
	}

	withStrangers := random(40)
	for range 5 {
		withStrangers[uuid.New()] = struct{}{}
	}

	return map[string]map[uuid.UUID]struct{}{
		"single":           pick(func(i int) bool { return i == 2 }),
		"five":             random(5),
		"fifth":            pick(func(i int) bool { return i%5 == 0 }),
		"half":             pick(func(i int) bool { return i%2 == 0 }),
		"at-threshold":     random(readableJoinThreshold),
		"above-threshold":  random(readableJoinThreshold + 1),
		"all":              pick(func(int) bool { return true }),
		"with-strangers":   withStrangers,
		"only-strangers":   {uuid.New(): {}, uuid.New(): {}},
		"deleted-included": pick(func(i int) bool { return i%17 == 16 || i < 3 }),
	}
}

func scopeFilters() map[string]SearchFilter {
	return map[string]SearchFilter{
		"everything":    {},
		"free-text":     {FreeText: "lib"},
		"narrow-text":   {FreeText: "pkg-1"},
		"name":          {Name: "lib-3"},
		"npm":           {Formats: []string{"npm"}},
		"raw-and-text":  {Formats: []string{"raw"}, FreeText: "lib"},
		"two-formats":   {Formats: []string{"npm", "raw"}, FreeText: "1"},
		"repo-filter":   {Repos: []string{"repo-3", "repo-4", "repo-6", "repo-9", "repo-12"}},
		"repo-and-text": {Repos: []string{"repo-3", "repo-4", "repo-6"}, FreeText: "lib"},
		"sha256":        {SHA256Prefix: "aaaa"},
		"sha256-miss":   {SHA256Prefix: "bbbb"},
		"no-match":      {FreeText: "zzzz"},
	}
}

// scopeResults is everything /api/v1/search derives from one (filter, readable
// set) pair.
type scopeResults struct {
	Pages        map[string][]SearchResult
	Totals       map[string]int64
	Count        int64
	Uncapped     []SearchResult
	Repositories []RepositoryResult
}

func collectScopeResults(t *testing.T, e *Executor, filter SearchFilter, readable map[uuid.UUID]struct{}) scopeResults {
	t.Helper()

	ctx := context.Background()
	out := scopeResults{Pages: map[string][]SearchResult{}, Totals: map[string]int64{}}

	for _, group := range []bool{false, true} {
		for _, page := range []struct{ limit, offset int }{{20, 0}, {7, 7}, {20, 100000}} {
			key := fmt.Sprintf("group=%v/limit=%d/offset=%d", group, page.limit, page.offset)

			res, total, err := e.SearchPage(ctx, filter, PageOptions{GroupByPackage: group}, page.limit, page.offset, readable)
			if err != nil {
				t.Fatalf("SearchPage %s: %v", key, err)
			}

			out.Pages[key], out.Totals[key] = res, total
		}
	}

	var err error

	if out.Count, err = e.CountResults(ctx, filter, readable); err != nil {
		t.Fatalf("CountResults: %v", err)
	}

	if out.Uncapped, err = e.Search(ctx, filter, 25, 3, readable); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if out.Repositories, err = e.SearchRepositories(ctx, filter, 30, 0, readable); err != nil {
		t.Fatalf("SearchRepositories: %v", err)
	}

	return out
}

// The same readable set must select the same rows, in the same order with the
// same totals, whether it scopes the query as an ANY list or as a join over
// the array: for every filter, and for principals whose sets are empty, small,
// large, past the threshold, or hold unknown and deleted repositories.
func TestReadableScopeFormsAreEquivalent(t *testing.T) {
	db := newMigratedSearchDB(t)
	all := seedScopeData(t, db)
	e := NewExecutor(db)

	restore := readableJoinThreshold
	t.Cleanup(func() { readableJoinThreshold = restore })

	var matched int

	for setName, readable := range principalSets(all) {
		for filterName, filter := range scopeFilters() {
			readableJoinThreshold = 1 << 30
			want := collectScopeResults(t, e, filter, readable)

			readableJoinThreshold = 0
			got := collectScopeResults(t, e, filter, readable)

			if !reflect.DeepEqual(want, got) {
				t.Errorf("set %q filter %q: the join form differs from the ANY form\nANY:  %+v\njoin: %+v", setName, filterName, want, got)
			}

			// The shipped threshold must agree too, whichever form it picks.
			readableJoinThreshold = 1000
			if def := collectScopeResults(t, e, filter, readable); !reflect.DeepEqual(want, def) {
				t.Errorf("set %q filter %q: the default threshold differs from the ANY form", setName, filterName)
			}

			matched += len(want.Uncapped)
		}
	}

	if matched == 0 {
		t.Fatal("no search matched anything: the seed data does not exercise the query")
	}
}

// The scope itself, not just agreement between its two forms: a principal sees
// only rows of repositories in its set, counted independently of the executor.
func TestReadableScopeSelectsExactlyTheSet(t *testing.T) {
	db := newMigratedSearchDB(t)
	all := seedScopeData(t, db)
	e := NewExecutor(db)

	restore := readableJoinThreshold
	t.Cleanup(func() { readableJoinThreshold = restore })

	ctx := context.Background()

	for setName, readable := range principalSets(all) {
		ids := make([]string, 0, len(readable))
		for id := range readable {
			ids = append(ids, id.String())
		}

		var want int64

		err := db.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM npm_versions nv JOIN npm_packages np ON np.id = nv.package_id
			        WHERE nv.deleted_at IS NULL AND np.repository_id = ANY($1::uuid[]))
			     + (SELECT count(*) FROM raw_artifacts ra WHERE ra.deleted_at IS NULL AND ra.repository_id = ANY($1::uuid[]))`,
			"{"+strings.Join(ids, ",")+"}").Scan(&want)
		if err != nil {
			t.Fatalf("expected count: %v", err)
		}

		for _, threshold := range []int{1 << 30, 0} {
			readableJoinThreshold = threshold

			got, err := e.CountResults(ctx, SearchFilter{}, readable)
			if err != nil {
				t.Fatalf("CountResults: %v", err)
			}

			if got != want {
				t.Errorf("set %q threshold %d: counted %d rows, the set holds %d", setName, threshold, got, want)
			}
		}
	}
}
