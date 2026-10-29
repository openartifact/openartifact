// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"sync"
	"testing"
	"time"
)

// debianGetter is one GetOrCreateDebian* call, reduced to what the tests compare.
type debianGetter struct {
	name  string
	table string
	get   func(ctx context.Context) (id string, createdAt, updatedAt time.Time, err error)
}

func debianDistributionGetter(pg *PostgresDB, repoID, name string) debianGetter {
	return debianGetter{"distribution", "debian_distributions", func(ctx context.Context) (string, time.Time, time.Time, error) {
		m, err := pg.GetOrCreateDebianDistribution(ctx, repoID, name)
		if err != nil {
			return "", time.Time{}, time.Time{}, err
		}

		return m.ID, m.CreatedAt, m.UpdatedAt, nil
	}}
}

func debianComponentGetter(pg *PostgresDB, distID, name string) debianGetter {
	return debianGetter{"component", "debian_components", func(ctx context.Context) (string, time.Time, time.Time, error) {
		m, err := pg.GetOrCreateDebianComponent(ctx, distID, name)
		if err != nil {
			return "", time.Time{}, time.Time{}, err
		}

		return m.ID, m.CreatedAt, m.UpdatedAt, nil
	}}
}

func debianArchitectureGetter(pg *PostgresDB, distID, name string) debianGetter {
	return debianGetter{"architecture", "debian_architectures", func(ctx context.Context) (string, time.Time, time.Time, error) {
		m, err := pg.GetOrCreateDebianArchitecture(ctx, distID, name)
		if err != nil {
			return "", time.Time{}, time.Time{}, err
		}

		return m.ID, m.CreatedAt, m.UpdatedAt, nil
	}}
}

// debianRowState is the part of a debian_* row a get-or-create must leave alone
// once the row exists: its version (xmin changes with every UPDATE, including a
// no-op "SET name = EXCLUDED.name") and its updated_at (a BEFORE UPDATE trigger
// bumps it).
type debianRowState struct {
	xmin      string
	updatedAt time.Time
}

// debianRowStateOf reads the row state of one id. table is a constant of the
// test, never user input.
func debianRowStateOf(tb testing.TB, pg *PostgresDB, table, id string) debianRowState {
	tb.Helper()

	var s debianRowState

	err := pg.GetDB().QueryRowContext(context.Background(),
		`SELECT xmin::text, updated_at FROM `+table+` WHERE id = $1`, id).Scan(&s.xmin, &s.updatedAt)
	if err != nil {
		tb.Fatalf("read %s row %s: %v", table, id, err)
	}

	return s
}

func debianRowCount(tb testing.TB, pg *PostgresDB, table string) int {
	tb.Helper()

	var n int
	if err := pg.GetDB().QueryRowContext(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		tb.Fatalf("count %s: %v", table, err)
	}

	return n
}

// requireDebianRowUntouched creates the row on the first call of g and asserts
// that asking for it again returns the same row, unchanged and unwritten. It
// returns the row id.
func requireDebianRowUntouched(t *testing.T, pg *PostgresDB, g debianGetter) string {
	t.Helper()

	var id string

	t.Run(g.name, func(t *testing.T) {
		ctx := context.Background()

		first, created, updated, err := g.get(ctx)
		if err != nil {
			t.Fatalf("first call: %v", err)
		}

		if first == "" || created.IsZero() || updated.IsZero() {
			t.Fatalf("first call must return the generated columns, got id=%q created=%v updated=%v", first, created, updated)
		}

		id = first
		before := debianRowStateOf(t, pg, g.table, id)

		for i := range 3 {
			again, createdAgain, updatedAgain, err := g.get(ctx)
			if err != nil {
				t.Fatalf("repeat call %d: %v", i, err)
			}

			if again != id || !createdAgain.Equal(created) || !updatedAgain.Equal(updated) {
				t.Errorf("repeat call %d returned (%s, %v, %v), want the first call's (%s, %v, %v)",
					i, again, createdAgain, updatedAgain, id, created, updated)
			}

			if after := debianRowStateOf(t, pg, g.table, id); after != before {
				t.Errorf("repeat call %d rewrote the row: xmin %s -> %s, updated_at %v -> %v",
					i, before.xmin, after.xmin, before.updatedAt, after.updatedAt)
			}
		}

		if n := debianRowCount(t, pg, g.table); n != 1 {
			t.Errorf("%s holds %d rows, want 1", g.table, n)
		}
	})

	return id
}

// TestGetOrCreateDebianLeavesExistingRowsUntouched pins what makes a repeated
// upload cheap: asking for a distribution, component or architecture that
// exists returns the same row, unchanged and unwritten. The upsert it replaced
// ("ON CONFLICT DO UPDATE SET name = EXCLUDED.name") took the row lock and wrote
// a new row version (a new xmin, a bumped updated_at, a recovery-journal write)
// on every upload.
func TestGetOrCreateDebianLeavesExistingRowsUntouched(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	repoID := newQuotaSeeder(t, pg, "deb-get-or-create", "deb").repo

	distID := requireDebianRowUntouched(t, pg, debianDistributionGetter(pg, repoID, "stable"))
	if distID == "" {
		t.Fatal("no distribution to hang the component and architecture on")
	}

	requireDebianRowUntouched(t, pg, debianComponentGetter(pg, distID, "main"))
	requireDebianRowUntouched(t, pg, debianArchitectureGetter(pg, distID, "amd64"))
}

// TestGetOrCreateDebianConcurrentFirstUploads: many uploads race to create the
// same distribution, component and architecture. Whichever insert wins, every
// caller gets the same id and none fails, and exactly one row exists.
func TestGetOrCreateDebianConcurrentFirstUploads(t *testing.T) {
	const callers = 32

	pg := newQuotaTestDB(t, callers+2)
	repoID := newQuotaSeeder(t, pg, "deb-get-or-create-race", "deb").repo

	// A distribution the component and architecture hang off. The distribution
	// itself is raced for under another name.
	seed, _, _, err := debianDistributionGetter(pg, repoID, "bookworm").get(context.Background())
	if err != nil {
		t.Fatalf("seed distribution: %v", err)
	}

	tests := []struct {
		debianGetter

		rows int // rows the table holds once the race is over
	}{
		{debianDistributionGetter(pg, repoID, "trixie"), 2}, // plus the seeded one
		{debianComponentGetter(pg, seed, "contrib"), 1},
		{debianArchitectureGetter(pg, seed, "arm64"), 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				wg    sync.WaitGroup
				start = make(chan struct{})
				ids   = make([]string, callers)
				errs  = make([]error, callers)
			)

			for i := range callers {
				wg.Go(func() {
					<-start

					ids[i], _, _, errs[i] = tc.get(context.Background())
				})
			}

			close(start)
			wg.Wait()

			for i := range callers {
				if errs[i] != nil {
					t.Fatalf("caller %d: %v", i, errs[i])
				}

				if ids[i] == "" || ids[i] != ids[0] {
					t.Fatalf("caller %d got id %q, caller 0 got %q: every caller must see the one row", i, ids[i], ids[0])
				}
			}

			if n := debianRowCount(t, pg, tc.table); n != tc.rows {
				t.Errorf("%s holds %d rows, want %d", tc.table, n, tc.rows)
			}
		})
	}
}

// BenchmarkGetOrCreateDebianUpload is the get-or-create work of one deb upload
// (distribution, component, architecture, all existing) under concurrent
// uploads to one distribution, the shape that serialized on the distribution row.
// wal-B/op is the write-ahead log the work generates: the upsert wrote a row
// version, its trigger side effects and a journal row per call.
func BenchmarkGetOrCreateDebianUpload(b *testing.B) {
	const conns = 16

	ctx := context.Background()
	pg := newQuotaTestDB(b, conns)
	repoID := newQuotaSeeder(b, pg, "deb-get-or-create-bench", "deb").repo

	dist := debianDistributionGetter(pg, repoID, "stable")

	distID, _, _, err := dist.get(ctx)
	if err != nil {
		b.Fatalf("seed distribution: %v", err)
	}

	getters := []debianGetter{
		dist, debianComponentGetter(pg, distID, "main"), debianArchitectureGetter(pg, distID, "amd64"),
	}

	for _, g := range getters {
		if _, _, _, err := g.get(ctx); err != nil {
			b.Fatalf("seed %s: %v", g.name, err)
		}
	}

	var startLSN string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&startLSN); err != nil {
		b.Fatalf("read wal position: %v", err)
	}

	b.SetParallelism(conns)
	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for _, g := range getters {
				if _, _, _, err := g.get(ctx); err != nil {
					b.Errorf("%s: %v", g.name, err)

					return
				}
			}
		}
	})

	b.StopTimer()

	var walBytes float64

	err = pg.GetDB().QueryRowContext(ctx, `SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), $1::pg_lsn)::float8`, startLSN).Scan(&walBytes)
	if err != nil {
		b.Fatalf("read wal position: %v", err)
	}

	b.ReportMetric(walBytes/float64(b.N), "wal-B/op")
}
