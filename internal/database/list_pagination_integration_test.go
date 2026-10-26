// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"slices"
	"testing"
)

// Rows inserted by one statement share created_at, so only the id tiebreaker
// keeps offset pages disjoint and complete.
func TestRepositoryListingPagesAreDisjointAndCompleteWithTiedTimestamps(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()

	if _, err := db.ExecContext(ctx, `INSERT INTO repositories (name, format, public, project_id, description)
		SELECT 'raw' || i, 'raw', i % 2 = 0, $1::uuid, '' FROM generate_series(1, 7) i`, systemOrgID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO repositories (name, format, project_id, description) SELECT 'npm' || i, 'npm', $1::uuid, '' FROM generate_series(1, 2) i`, systemOrgID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO repositories (name, format, public, deleted_at, project_id, description) VALUES ('gone', 'raw', true, now(), $1, '')`, systemOrgID); err != nil {
		t.Fatal(err)
	}

	walk := func(format string, limit int) []string {
		var names []string

		for page := 1; page < 20; page++ {
			repos, err := pg.ListRepositories(ctx, page, limit, format)
			if err != nil {
				t.Fatal(err)
			}

			if len(repos) == 0 {
				return names
			}

			for _, r := range repos {
				names = append(names, r.Name)
			}
		}

		t.Fatal("pagination does not terminate")

		return nil
	}

	for _, tc := range []struct {
		format string
		want   int
	}{{"", 9}, {"raw", 7}, {"npm", 2}, {"maven", 0}} {
		names := walk(tc.format, 4)

		sorted := slices.Clone(names)
		slices.Sort(sorted)

		if len(slices.Compact(sorted)) != len(names) || len(names) != tc.want {
			t.Errorf("format %q: walk = %v (%d rows), want %d distinct", tc.format, names, len(names), tc.want)
		}

		if slices.Contains(names, "gone") {
			t.Errorf("format %q listed a soft-deleted repository", tc.format)
		}

		if n, err := pg.CountRepositories(ctx, tc.format); err != nil || n != tc.want {
			t.Errorf("CountRepositories(%q) = %d, %v; want %d", tc.format, n, err, tc.want)
		}
	}

	if !slices.Equal(walk("", 4), walk("", 100)) {
		t.Error("paged order differs from the single-page order")
	}

	checkPublicAndByName(t, pg)
}

func checkPublicAndByName(t *testing.T, pg *PostgresDB) {
	t.Helper()

	ctx := context.Background()

	public, err := pg.ListPublicRepositoryNames(ctx)
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(public)

	if !slices.Equal(public, []string{"raw2", "raw4", "raw6"}) {
		t.Errorf("public names = %v, want the live public repositories only", public)
	}

	if r, err := pg.GetRepositoryByName(ctx, "gone"); r != nil || err != nil {
		t.Errorf("GetRepositoryByName(soft-deleted) = %v, %v; want nil, nil", r, err)
	}

	if r, err := pg.GetRepositoryByName(ctx, "absent"); r != nil || err != nil {
		t.Errorf("GetRepositoryByName(absent) = %v, %v; want nil, nil", r, err)
	}
}

func TestListUsersPagesAndTotal(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)

	if _, err := pg.GetDB().ExecContext(ctx, `INSERT INTO auth_users (username) SELECT 'u' || i FROM generate_series(1, 5) i`); err != nil {
		t.Fatal(err)
	}

	var seen []string

	for page := 1; page <= 3; page++ {
		users, total, err := pg.ListUsers(ctx, page, 2)
		if err != nil {
			t.Fatal(err)
		}

		if total != 5 {
			t.Errorf("page %d total = %d, want 5 humans", page, total)
		}

		for _, u := range users {
			seen = append(seen, u.Username)
		}
	}

	slices.Sort(seen)

	if !slices.Equal(seen, []string{"u1", "u2", "u3", "u4", "u5"}) {
		t.Errorf("paged users = %v, want u1..u5 once each, humans only", seen)
	}

	if users, _, err := pg.ListUsers(ctx, 4, 2); err != nil || len(users) != 0 {
		t.Errorf("page past the end = %d users, %v", len(users), err)
	}
}

func TestListProjectRepositoriesCountsOnlyLiveOwnedRepositories(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()

	var mine, other string
	if err := db.QueryRowContext(ctx, `INSERT INTO projects (name, slug) VALUES ('mine','mine') RETURNING id`).Scan(&mine); err != nil {
		t.Fatal(err)
	}

	if err := db.QueryRowContext(ctx, `INSERT INTO projects (name, slug) VALUES ('other','other') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO repositories (name, format, project_id, description) SELECT 'm' || i, 'raw', $1::uuid, '' FROM generate_series(1, 5) i`, mine); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO repositories (name, format, project_id, deleted_at, description) VALUES ('mdead', 'raw', $1, now(), ''), ('o1', 'raw', $2, NULL, '')`, mine, other); err != nil {
		t.Fatal(err)
	}

	var names []string

	for page := 1; page <= 3; page++ {
		repos, total, err := pg.ListProjectRepositories(ctx, mine, page, 2)
		if err != nil {
			t.Fatal(err)
		}

		if total != 5 {
			t.Errorf("total = %d, want 5", total)
		}

		for _, r := range repos {
			names = append(names, r.Name)
		}
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"m1", "m2", "m3", "m4", "m5"}) {
		t.Errorf("names = %v", names)
	}
}
