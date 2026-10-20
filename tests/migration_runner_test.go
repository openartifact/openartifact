// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"sync"
	"testing"
	"testing/fstest"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/openartifact/openartifact/internal/database"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		skipOrFailMissingEnv(t, "TEST_DATABASE_URL")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()

		t.Fatalf("db.Ping: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS schema_migrations`)
		_ = db.Close()
	})

	return db
}

func makeFakeFS(files map[string]string) fs.ReadDirFS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}

	return m
}

func TestApplyIdempotent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_create_test.sql": `CREATE TABLE IF NOT EXISTS migration_test_a (id SERIAL PRIMARY KEY)`,
	})
	m := database.NewMigratorWithFS(db, fsys)

	if err := m.Apply(ctx); err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	var count1 int

	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count1)

	if err := m.Apply(ctx); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	var count2 int

	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count2)

	if count1 != count2 {
		t.Errorf("row count changed after idempotent apply: %d → %d", count1, count2)
	}

	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS migration_test_a`)
}

func TestChecksumDrift(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_drift_test.sql": `CREATE TABLE IF NOT EXISTS migration_test_b (id SERIAL PRIMARY KEY)`,
	})

	m := database.NewMigratorWithFS(db, fsys)
	if err := m.Apply(ctx); err != nil {
		t.Fatalf("initial apply: %v", err)
	}

	_, _ = db.ExecContext(ctx, `UPDATE schema_migrations SET checksum='badhash' WHERE version='001_drift_test.sql'`)

	if err := m.Apply(ctx); err == nil {
		t.Fatal("expected drift error, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}

	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS migration_test_b`)
}

func TestStatusPending(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_status_a.sql": `CREATE TABLE IF NOT EXISTS migration_test_c (id SERIAL PRIMARY KEY)`,
		"002_status_b.sql": `CREATE TABLE IF NOT EXISTS migration_test_d (id SERIAL PRIMARY KEY)`,
	})
	m := database.NewMigratorWithFS(db, fsys)

	fsys1 := makeFakeFS(map[string]string{
		"001_status_a.sql": `CREATE TABLE IF NOT EXISTS migration_test_c (id SERIAL PRIMARY KEY)`,
		"002_status_b.sql": `CREATE TABLE IF NOT EXISTS migration_test_d (id SERIAL PRIMARY KEY)`,
	})

	m1 := database.NewMigratorWithFS(db, fsys1)
	if err := m1.Apply(ctx); err != nil {
		t.Fatalf("apply 001 only: %v", err)
	}

	_, _ = db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version='002_status_b.sql'`)

	statuses, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	var pendingCount int

	for _, s := range statuses {
		if s.Version == "002_status_b.sql" && s.Pending {
			pendingCount++
		}
	}

	if pendingCount == 0 {
		t.Error("expected 002_status_b.sql to be pending")
	}

	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS migration_test_c`)
	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS migration_test_d`)
}

func TestTransactionRollback(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_rollback_test.sql": `CREATE TABLE IF NOT EXISTS migration_test_e (id SERIAL PRIMARY KEY); INVALID SQL THAT FAILS;`,
	})

	m := database.NewMigratorWithFS(db, fsys)
	if err := m.Apply(ctx); err == nil {
		t.Fatal("expected error from invalid SQL, got nil")
	}

	var count int

	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='001_rollback_test.sql'`).Scan(&count)
	if count != 0 {
		t.Errorf("expected version not recorded, but found %d row(s)", count)
	}
}

func TestNoTxDetection(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_notx.no_tx.sql": `CREATE TABLE IF NOT EXISTS migration_test_f (id SERIAL PRIMARY KEY)`,
	})

	m := database.NewMigratorWithFS(db, fsys)
	if err := m.Apply(ctx); err != nil {
		t.Fatalf("Apply with no_tx file: %v", err)
	}

	var count int

	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='001_notx.no_tx.sql'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected no_tx migration recorded, got %d", count)
	}

	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS migration_test_f`)
}

func TestAdvisoryLockConcurrent(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_concurrent.sql": `CREATE TABLE IF NOT EXISTS migration_test_g (id SERIAL PRIMARY KEY)`,
	})

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			m := database.NewMigratorWithFS(db, fsys)
			errs[idx] = m.Apply(ctx)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d Apply error: %v", i, err)
		}
	}

	var count int

	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='001_concurrent.sql'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 schema_migrations row for concurrent test, got %d", count)
	}

	_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS migration_test_g`)
}
