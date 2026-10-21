// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const systemOrgID = "00000000-0000-0000-0000-000000000001"

// newQuotaTestDB creates an empty database on the TEST_DATABASE_URL server,
// applies the embedded migrations and returns a PostgresDB over it. The test is
// skipped (or failed under CI) when TEST_DATABASE_URL is not set.
func newQuotaTestDB(tb testing.TB, maxConns int) *PostgresDB {
	tb.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			tb.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		tb.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		tb.Fatalf("open admin connection: %v", err)
	}

	tb.Cleanup(func() { _ = admin.Close() })

	dbName := fmt.Sprintf("oa_quota_%d", time.Now().UnixNano())

	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
		tb.Fatalf("create database: %v", err)
	}

	tb.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		tb.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		tb.Fatalf("create logger: %v", err)
	}

	pg, err := NewPostgresDB(DatabaseConfig{
		Host:         u.Hostname(),
		Port:         port,
		User:         u.User.Username(),
		Password:     pw,
		Database:     dbName,
		SSLMode:      sslmode,
		MaxOpenConns: maxConns,
		MaxIdleConns: maxConns,
	}, log)
	if err != nil {
		tb.Fatalf("NewPostgresDB: %v", err)
	}

	tb.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		tb.Fatalf("apply migrations: %v", err)
	}

	return pg
}

// newQuotaRepo creates a repository and, when maxBytes or maxCount is non-zero,
// its quota row with those limits. It returns the repository ID.
func newQuotaRepo(tb testing.TB, pg *PostgresDB, name string, maxBytes int64, maxCount int) string {
	tb.Helper()

	ctx := context.Background()
	org := systemOrgID

	repo := &models.Repository{Name: name, Format: "raw", RepoType: "hosted", OrgID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		tb.Fatalf("CreateRepository: %v", err)
	}

	if maxBytes == 0 && maxCount == 0 {
		return repo.ID
	}

	q, err := pg.GetOrCreateStorageQuota(ctx, repo.ID)
	if err != nil {
		tb.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	q.MaxSizeBytes, q.MaxArtifactCount = maxBytes, maxCount
	if err := pg.UpdateStorageQuota(ctx, q); err != nil {
		tb.Fatalf("UpdateStorageQuota: %v", err)
	}

	return repo.ID
}

// quotaUsage reads the repository's counters straight from the table (not via
// GetOrCreateStorageQuota, which would create a missing row).
func quotaUsage(tb testing.TB, pg *PostgresDB, repoID string) (int64, int64, bool) {
	tb.Helper()

	var size, count int64

	err := pg.GetDB().QueryRowContext(context.Background(),
		`SELECT current_size_bytes, current_artifact_count FROM storage_quotas WHERE repository_id = $1`, repoID,
	).Scan(&size, &count)
	if err == sql.ErrNoRows {
		return 0, 0, false
	}

	if err != nil {
		tb.Fatalf("read usage: %v", err)
	}

	return size, count, true
}

// reserveConcurrently runs n goroutines that each reserve (size, 1) at the same
// instant and returns how many succeeded.
func reserveConcurrently(tb testing.TB, pg *PostgresDB, repoID string, n int, size int64) int64 {
	tb.Helper()

	var (
		ok    atomic.Int64
		wg    sync.WaitGroup
		start = make(chan struct{})
	)

	for range n {
		wg.Go(func() {
			<-start

			got, err := pg.ReserveStorageUsage(context.Background(), repoID, size, 1)
			if err != nil {
				tb.Errorf("ReserveStorageUsage: %v", err)

				return
			}

			if got {
				ok.Add(1)
			}
		})
	}

	close(start)
	wg.Wait()

	return ok.Load()
}

func TestReserveStorageUsageEnforcesLimitsUnderContention(t *testing.T) {
	pg := newQuotaTestDB(t, 80)

	const (
		goroutines = 64
		fit        = 10
		size       = int64(100)
	)

	tests := []struct {
		name     string
		maxBytes int64
		maxCount int
		wantOK   int64
	}{
		{"size limit fits exactly K", fit * size, 0, fit},
		{"size limit one byte short of K+1", fit*size + size - 1, 0, fit},
		{"artifact count limit fits exactly K", 0, fit, fit},
		{"both limits, count binds first", 1000 * size, fit - 3, fit - 3},
		{"both limits, size binds first", (fit - 3) * size, 1000, fit - 3},
		{"unlimited", 0, 0, goroutines},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoID := newQuotaRepo(t, pg, fmt.Sprintf("race-%d", i), tc.maxBytes, tc.maxCount)
			if tc.maxBytes == 0 && tc.maxCount == 0 {
				// Unlimited via an explicit zero-limit quota row.
				if _, err := pg.GetOrCreateStorageQuota(context.Background(), repoID); err != nil {
					t.Fatalf("GetOrCreateStorageQuota: %v", err)
				}
			}

			got := reserveConcurrently(t, pg, repoID, goroutines, size)
			if got != tc.wantOK {
				t.Fatalf("%d reservations succeeded, want exactly %d", got, tc.wantOK)
			}

			usedBytes, usedCount, _ := quotaUsage(t, pg, repoID)
			if usedBytes != got*size || usedCount != got {
				t.Fatalf("usage = %d bytes / %d artifacts, want %d / %d", usedBytes, usedCount, got*size, got)
			}

			if tc.maxBytes > 0 && usedBytes > tc.maxBytes {
				t.Errorf("used %d bytes exceeds the %d byte limit", usedBytes, tc.maxBytes)
			}

			if tc.maxCount > 0 && usedCount > int64(tc.maxCount) {
				t.Errorf("used %d artifacts exceeds the limit %d", usedCount, tc.maxCount)
			}

			// Give every successful reservation back: usage returns to zero and a
			// full set of new reservations fits again.
			for range got {
				if err := pg.DecrementStorageUsage(context.Background(), repoID, size, 1); err != nil {
					t.Fatalf("DecrementStorageUsage: %v", err)
				}
			}

			if b, c, _ := quotaUsage(t, pg, repoID); b != 0 || c != 0 {
				t.Fatalf("usage after release = %d / %d, want 0 / 0", b, c)
			}

			if again := reserveConcurrently(t, pg, repoID, goroutines, size); again != tc.wantOK {
				t.Errorf("after release %d reservations succeeded, want %d", again, tc.wantOK)
			}
		})
	}
}

func TestReserveStorageUsageNeverExceedsLimitWhileReleasing(t *testing.T) {
	pg := newQuotaTestDB(t, 80)

	const (
		workers = 32
		rounds  = 25
		size    = int64(100)
		limit   = 5 * size
	)

	repoID := newQuotaRepo(t, pg, "churn", limit, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		overshoot atomic.Int64
		sampled   = make(chan struct{})
	)

	go func() {
		defer close(sampled)

		for ctx.Err() == nil {
			var used int64

			err := pg.GetDB().QueryRowContext(ctx,
				`SELECT current_size_bytes FROM storage_quotas WHERE repository_id = $1`, repoID).Scan(&used)
			if err == nil && used > limit {
				overshoot.Store(used)
			}
		}
	}()

	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			for range rounds {
				ok, err := pg.ReserveStorageUsage(context.Background(), repoID, size, 1)
				if err != nil {
					t.Errorf("ReserveStorageUsage: %v", err)

					return
				}

				if ok {
					if err := pg.DecrementStorageUsage(context.Background(), repoID, size, 1); err != nil {
						t.Errorf("DecrementStorageUsage: %v", err)

						return
					}
				}
			}
		})
	}

	wg.Wait()
	cancel()
	<-sampled

	if v := overshoot.Load(); v != 0 {
		t.Errorf("observed %d used bytes, above the %d limit", v, limit)
	}

	if b, c, _ := quotaUsage(t, pg, repoID); b != 0 || c != 0 {
		t.Errorf("usage after balanced reserve/release = %d / %d, want 0 / 0", b, c)
	}
}

func TestReserveStorageUsageWithoutQuotaRow(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()

	t.Run("creates an unlimited row", func(t *testing.T) {
		repoID := newQuotaRepo(t, pg, "norow", 0, 0)

		if _, _, found := quotaUsage(t, pg, repoID); found {
			t.Fatal("precondition: repository must not have a quota row yet")
		}

		ok, err := pg.ReserveStorageUsage(ctx, repoID, 1<<40, 1)
		if err != nil || !ok {
			t.Fatalf("ReserveStorageUsage = %v, %v; want true, nil (no row means unlimited)", ok, err)
		}

		if b, c, found := quotaUsage(t, pg, repoID); !found || b != 1<<40 || c != 1 {
			t.Errorf("usage = %d / %d (found %v), want %d / 1", b, c, found, int64(1)<<40)
		}

		// A later limit applies to what was already used.
		q, err := pg.GetOrCreateStorageQuota(ctx, repoID)
		if err != nil {
			t.Fatalf("GetOrCreateStorageQuota: %v", err)
		}

		q.MaxSizeBytes = 1<<40 + 10
		if err := pg.UpdateStorageQuota(ctx, q); err != nil {
			t.Fatalf("UpdateStorageQuota: %v", err)
		}

		if ok, err := pg.ReserveStorageUsage(ctx, repoID, 11, 1); err != nil || ok {
			t.Errorf("reservation past the new limit = %v, %v; want false, nil", ok, err)
		}

		if ok, err := pg.ReserveStorageUsage(ctx, repoID, 10, 1); err != nil || !ok {
			t.Errorf("reservation exactly to the limit = %v, %v; want true, nil", ok, err)
		}
	})

	t.Run("concurrent first reservations create one row", func(t *testing.T) {
		repoID := newQuotaRepo(t, pg, "norow-race", 0, 0)

		if got := reserveConcurrently(t, pg, repoID, 16, 7); got != 16 {
			t.Fatalf("%d of 16 reservations succeeded on an unlimited repo", got)
		}

		if b, c, _ := quotaUsage(t, pg, repoID); b != 16*7 || c != 16 {
			t.Errorf("usage = %d / %d, want %d / 16", b, c, 16*7)
		}
	})

	t.Run("unknown repository is an error, not over-quota", func(t *testing.T) {
		ok, err := pg.ReserveStorageUsage(ctx, "11111111-1111-1111-1111-111111111111", 1, 1)
		if err == nil || ok {
			t.Errorf("ReserveStorageUsage = %v, %v; want false and an error", ok, err)
		}
	})

	t.Run("release without a row is a no-op", func(t *testing.T) {
		repoID := newQuotaRepo(t, pg, "norow-release", 0, 0)

		if err := pg.DecrementStorageUsage(ctx, repoID, 5, 1); err != nil {
			t.Fatalf("DecrementStorageUsage: %v", err)
		}

		if _, _, found := quotaUsage(t, pg, repoID); found {
			t.Error("a release must not create a quota row")
		}
	})
}

func TestDecrementStorageUsageNeverGoesNegative(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()
	repoID := newQuotaRepo(t, pg, "floor", 1000, 10)

	if ok, err := pg.ReserveStorageUsage(ctx, repoID, 30, 1); err != nil || !ok {
		t.Fatalf("ReserveStorageUsage = %v, %v", ok, err)
	}

	// Releasing more than was reserved (a double release, or a delete of an
	// artifact uploaded before the counters existed) clamps at zero.
	if err := pg.DecrementStorageUsage(ctx, repoID, 100, 5); err != nil {
		t.Fatalf("DecrementStorageUsage: %v", err)
	}

	if b, c, _ := quotaUsage(t, pg, repoID); b != 0 || c != 0 {
		t.Errorf("usage = %d / %d, want 0 / 0", b, c)
	}

	// A failed upload releases exactly what it reserved and frees the space.
	if ok, err := pg.ReserveStorageUsage(ctx, repoID, 1000, 1); err != nil || !ok {
		t.Fatalf("filling the quota = %v, %v", ok, err)
	}

	if ok, err := pg.ReserveStorageUsage(ctx, repoID, 1, 1); err != nil || ok {
		t.Fatalf("reservation on a full quota = %v, %v; want false, nil", ok, err)
	}

	if err := pg.DecrementStorageUsage(ctx, repoID, 1000, 1); err != nil {
		t.Fatalf("DecrementStorageUsage: %v", err)
	}

	if ok, err := pg.ReserveStorageUsage(ctx, repoID, 1, 1); err != nil || !ok {
		t.Errorf("reservation after release = %v, %v; want true, nil", ok, err)
	}
}

// BenchmarkReserveRelease measures the reservation path (reserve, then release
// to keep the row from filling up) on one hot repository row with 32
// concurrent uploaders; reserve-ns/op is the mean latency of the reservation.
func BenchmarkReserveRelease(b *testing.B) {
	const uploaders = 32

	pg := newQuotaTestDB(b, uploaders+8)
	repoID := newQuotaRepo(b, pg, "bench", 0, 0)
	ctx := context.Background()

	var (
		next     atomic.Int64
		reserved atomic.Int64 // nanoseconds spent in ReserveStorageUsage
		wg       sync.WaitGroup
	)

	b.ResetTimer()

	for range uploaders {
		wg.Go(func() {
			for next.Add(1) <= int64(b.N) {
				t0 := time.Now()
				ok, err := pg.ReserveStorageUsage(ctx, repoID, 10, 1)
				reserved.Add(int64(time.Since(t0)))

				if err != nil || !ok {
					b.Errorf("ReserveStorageUsage = %v, %v", ok, err)

					return
				}

				if err := pg.DecrementStorageUsage(ctx, repoID, 10, 1); err != nil {
					b.Errorf("DecrementStorageUsage: %v", err)

					return
				}
			}
		})
	}

	wg.Wait()
	b.ReportMetric(float64(reserved.Load())/float64(b.N), "reserve-ns/op")
}
