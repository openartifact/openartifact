// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// newMigratorWithFS returns a Migrator over a custom migrations FS.
func newMigratorWithFS(db *sql.DB, fsys fs.ReadDirFS) *Migrator {
	return &Migrator{db: db, fs: fsys}
}

func makeFakeFS(files map[string]string) fs.ReadDirFS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}

	return m
}

func TestApplyIdempotent(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_create_test.sql": `CREATE TABLE IF NOT EXISTS migration_test_a (id SERIAL PRIMARY KEY)`,
	})
	m := newMigratorWithFS(db, fsys)

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
}

func TestChecksumDrift(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_drift_test.sql": `CREATE TABLE IF NOT EXISTS migration_test_b (id SERIAL PRIMARY KEY)`,
	})

	m := newMigratorWithFS(db, fsys)
	if err := m.Apply(ctx); err != nil {
		t.Fatalf("initial apply: %v", err)
	}

	_, _ = db.ExecContext(ctx, `UPDATE schema_migrations SET checksum='badhash' WHERE version='001_drift_test.sql'`)

	if err := m.Apply(ctx); err == nil {
		t.Fatal("expected drift error, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

func TestTransactionRollback(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_rollback_test.sql": `CREATE TABLE IF NOT EXISTS migration_test_e (id SERIAL PRIMARY KEY); INVALID SQL THAT FAILS;`,
	})

	m := newMigratorWithFS(db, fsys)
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
	db := scratchDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_notx.no_tx.sql": `CREATE TABLE IF NOT EXISTS migration_test_f (id SERIAL PRIMARY KEY)`,
	})

	m := newMigratorWithFS(db, fsys)
	if err := m.Apply(ctx); err != nil {
		t.Fatalf("Apply with no_tx file: %v", err)
	}

	var count int

	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='001_notx.no_tx.sql'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected no_tx migration recorded, got %d", count)
	}
}

// A no_tx file with several statements, a dollar-quoted body holding semicolons
// and CREATE INDEX CONCURRENTLY: each statement must reach the server as its own
// query, since a multi-statement query runs in one implicit transaction.
func TestNoTxRunsStatementsSeparately(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_multi.no_tx.sql": `-- comment; with a semicolon
CREATE TABLE migration_test_h (id int, v int);
CREATE INDEX CONCURRENTLY migration_test_h_v ON migration_test_h (v);
DO $$ BEGIN PERFORM 1; PERFORM 2; END $$;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS migration_test_h_id ON migration_test_h (id) WHERE v IS NULL;
INSERT INTO migration_test_h VALUES (1, NULL), (1, 2)`,
	})

	if err := newMigratorWithFS(db, fsys).Apply(ctx); err != nil {
		t.Fatalf("Apply multi-statement no_tx file: %v", err)
	}

	var indexes int

	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM pg_indexes WHERE tablename = 'migration_test_h' AND indexname LIKE 'migration_test_h_%'`).Scan(&indexes)
	if indexes != 2 {
		t.Errorf("created %d indexes, want 2", indexes)
	}

	var rows int

	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM migration_test_h`).Scan(&rows)
	if rows != 2 {
		t.Errorf("last statement inserted %d rows, want 2", rows)
	}
}

// A failing statement of a no_tx file stops the file, names the statement and
// leaves the version unrecorded so the next start retries it.
func TestNoTxFailureIsNotRecorded(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()

	fsys := makeFakeFS(map[string]string{
		"001_fail.no_tx.sql": `CREATE TABLE migration_test_i (id int); SELECT * FROM migration_test_missing; CREATE INDEX CONCURRENTLY migration_test_i_id ON migration_test_i (id)`,
	})

	err := newMigratorWithFS(db, fsys).Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "statement 2") {
		t.Fatalf("Apply = %v, want an error naming statement 2", err)
	}

	var count int

	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = '001_fail.no_tx.sql'`).Scan(&count)
	if count != 0 {
		t.Errorf("failed no_tx migration recorded %d times", count)
	}
}

func TestAdvisoryLockConcurrent(t *testing.T) {
	db := scratchDB(t)
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

			errs[idx] = newMigratorWithFS(db, fsys).Apply(ctx)
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
}
