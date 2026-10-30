// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"slices"
	"testing"
	"time"
)

// seedTiedVersions writes n live versions (one file each) of one npm package
// where perSecond consecutive versions share one created_at, so the keyset has
// to break ties by id.
func seedTiedVersions(t *testing.T, f *catalogFixture, name string, n, perSecond int, created time.Time) {
	t.Helper()

	_, err := f.db.ExecContext(context.Background(), `
		WITH p AS (
			INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'npm', $2, $2) RETURNING id
		), v AS (
			INSERT INTO package_versions (package_id, repository_id, version, version_key, created_at, updated_at)
			SELECT p.id, $1, '1.' || g, '1.' || g, $4::timestamptz + (g / $5::int) * interval '1 second', $4::timestamptz
			FROM p, generate_series(1, $3::int) g
			RETURNING id, version, created_at
		)
		INSERT INTO package_files (version_id, repository_id, path, filename, created_at, updated_at)
		SELECT id, $1, $2 || '/' || version, $2 || '-' || version || '.tgz', created_at, created_at FROM v`,
		f.repo, name, n, created, perSecond)
	if err != nil {
		t.Fatalf("seed tied versions: %v", err)
	}
}

// seedTiedNonFileRows adds n docker tags and n proxy cache entries whose
// created_at fall on the same seconds as the files seeded by seedTiedVersions.
func seedTiedNonFileRows(t *testing.T, f *catalogFixture, n int, created time.Time) {
	t.Helper()

	ctx := context.Background()

	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO docker_tags (repository_id, name, manifest_digest, image, created_at, updated_at)
		SELECT $1, 'tag' || g, 'sha256:' || lpad(g::text, 64, '0'), 'img', $3::timestamptz + (g / 4) * interval '1 second', $3::timestamptz
		FROM generate_series(1, $2::int) g`, f.repo, n, created); err != nil {
		t.Fatalf("seed docker tags: %v", err)
	}

	if _, err := f.db.ExecContext(ctx, `
		INSERT INTO proxy_cache_entries (repository_id, cache_key, blob_key, expires_at, created_at, updated_at)
		SELECT $1, 'path/file-' || g, repeat('a', 64), now() + interval '1 day', $3::timestamptz + (g / 4) * interval '1 second', $3::timestamptz
		FROM generate_series(1, $2::int) g`, f.repo, n, created); err != nil {
		t.Fatalf("seed proxy cache: %v", err)
	}
}

func offsetWalk(t *testing.T, f *catalogFixture, opts ComponentListOptions, limit int) []string {
	t.Helper()

	var ids []string

	for page := 1; ; page++ {
		opts.Page, opts.Limit = page, limit

		rows, total, err := f.pg.SearchComponents(context.Background(), f.repo, opts)
		if err != nil {
			t.Fatal(err)
		}

		for _, r := range rows {
			ids = append(ids, r.ID)
		}

		if page*limit >= total {
			return ids
		}
	}
}

// cursorWalk pages the way the API does: limit+1 rows per request, no total,
// the cursor of the last row kept. It returns the ids and the has_more flag of
// every page.
func cursorWalk(t *testing.T, f *catalogFixture, opts ComponentListOptions, limit int) ([]string, []bool) {
	t.Helper()

	var (
		ids  []string
		more []bool
	)

	opts.NoTotal = true

	for {
		opts.Page, opts.Limit = 1, limit+1

		rows, total, err := f.pg.SearchComponents(context.Background(), f.repo, opts)
		if err != nil {
			t.Fatal(err)
		}

		if total != 0 {
			t.Fatalf("NoTotal returned total %d", total)
		}

		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}

		for _, r := range rows {
			ids = append(ids, r.ID)
		}

		more = append(more, hasMore)

		if !hasMore || len(more) > 1000 {
			return ids, more
		}

		last := rows[len(rows)-1]
		opts.After = &ComponentCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
}

// TestComponentKeysetMatchesOffset: walking the newest-first listing by cursor
// yields exactly the rows of the offset walk, in the same order, with no
// duplicate and no gap, even when many rows share one created_at, across the
// file, docker tag and proxy cache branches and with every filter.
func TestComponentKeysetMatchesOffset(t *testing.T) {
	f := newCatalogFixture(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)

	seedTiedVersions(t, f, "alpha", 60, 8, base)
	seedTiedVersions(t, f, "beta", 60, 8, base)
	// more than the narrow bound of q, so q walks the listing index
	seedTiedVersions(t, f, "bulk", 2100, 50, base)
	seedTiedNonFileRows(t, f, 24, base)

	for _, tc := range []struct {
		name string
		opts ComponentListOptions
		want int
	}{
		{"everything", ComponentListOptions{}, 60 + 60 + 2100 + 24 + 24},
		{"narrow q", ComponentListOptions{Query: "alpha"}, 60},
		{"broad q", ComponentListOptions{Query: "bulk"}, 2100},
		{"q on a version", ComponentListOptions{Query: "1.1"}, -1},
		// the proxy cache rows belong to the repository's own format
		{"format npm", ComponentListOptions{Format: "npm"}, 60 + 60 + 2100 + 24},
		{"format docker", ComponentListOptions{Format: "docker"}, 24},
		{"package name", ComponentListOptions{Name: "beta"}, 60},
		{"package name and format", ComponentListOptions{Name: "beta", Format: "npm"}, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, limit := range []int{37, 250} {
				want := offsetWalk(t, f, tc.opts, limit)
				if tc.want >= 0 && len(want) != tc.want {
					t.Fatalf("offset walk found %d rows, want %d", len(want), tc.want)
				}

				got, more := cursorWalk(t, f, tc.opts, limit)
				if !slices.Equal(got, want) {
					t.Fatalf("limit %d: cursor walk found %d rows, offset walk %d; they differ", limit, len(got), len(want))
				}

				if more[len(more)-1] || slices.Contains(more[:len(more)-1], false) {
					t.Fatalf("limit %d: has_more per page %v, want true until the last page", limit, more)
				}

				seen := map[string]bool{}
				for _, id := range got {
					if seen[id] {
						t.Fatalf("limit %d: duplicate row %s", limit, id)
					}

					seen[id] = true
				}
			}
		})
	}
}

// TestComponentKeysetStartsAnywhere: a cursor taken from the last row of any
// offset page continues with the next offset page.
func TestComponentKeysetStartsAnywhere(t *testing.T) {
	f := newCatalogFixture(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)

	seedTiedVersions(t, f, "alpha", 100, 10, base)
	seedTiedNonFileRows(t, f, 12, base)

	ctx := context.Background()

	for page := 1; page <= 4; page++ {
		rows, _, err := f.pg.SearchComponents(ctx, f.repo, ComponentListOptions{Page: page, Limit: 9, NoTotal: true})
		if err != nil {
			t.Fatal(err)
		}

		next, _, err := f.pg.SearchComponents(ctx, f.repo, ComponentListOptions{Page: page + 1, Limit: 9, NoTotal: true})
		if err != nil {
			t.Fatal(err)
		}

		last := rows[len(rows)-1]

		after, _, err := f.pg.SearchComponents(ctx, f.repo, ComponentListOptions{
			After: &ComponentCursor{CreatedAt: last.CreatedAt, ID: last.ID}, Page: 99, Limit: 9, NoTotal: true,
		})
		if err != nil {
			t.Fatal(err)
		}

		if len(after) != len(next) {
			t.Fatalf("page %d: cursor gave %d rows, offset %d", page, len(after), len(next))
		}

		for i := range after {
			if after[i].ID != next[i].ID {
				t.Fatalf("page %d row %d: cursor %s, offset %s", page, i, after[i].ID, next[i].ID)
			}
		}
	}
}
