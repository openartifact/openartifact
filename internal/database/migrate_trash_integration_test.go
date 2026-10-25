// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/migrations"
)

const (
	trashSchemaMigration  = "017_trash.sql"
	trashIndexesMigration = "018_trash_indexes.no_tx.sql"
	pgCheckViolation      = "23514"
	pgNoArbiterIndex      = "42P10"
)

// scratchDB creates an empty database on the TEST_DATABASE_URL server and
// returns a pool over it; the database is dropped when the test ends.
func scratchDB(t *testing.T) *sql.DB {
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

	dbName := fmt.Sprintf("oa_migrate_%d", time.Now().UnixNano())

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

	u.Path = "/" + dbName

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}

	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

// migrationsThrough is the embedded migrations without the files numbered
// after last ("016"): the schema an older release left behind.
type migrationsThrough struct{ last string }

func (m migrationsThrough) Open(name string) (fs.File, error) { return migrations.FS.Open(name) }

func (m migrationsThrough) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := migrations.FS.ReadDir(name)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(entries, func(e fs.DirEntry) bool {
		version, _, _ := strings.Cut(e.Name(), "_")

		return version > m.last
	}), nil
}

func migrateThrough(t *testing.T, db *sql.DB, last string) {
	t.Helper()

	if err := newMigratorWithFS(db, migrationsThrough{last}).Apply(context.Background()); err != nil {
		t.Fatalf("apply migrations through %s: %v", last, err)
	}
}

func migrateAll(t *testing.T, db *sql.DB) {
	t.Helper()

	if err := NewMigrator(db).Apply(context.Background()); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()

	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustScan[T any](t *testing.T, db *sql.DB, query string, args ...any) T {
	t.Helper()

	var v T
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}

	return v
}

// wantPgCode fails unless err is a PostgreSQL error with the given SQLSTATE.
func wantPgCode(t *testing.T, err error, code, what string) {
	t.Helper()

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("%s: err = %v, want SQLSTATE %s", what, err, code)
	}
}

// seededRows are the rows written under the pre-trash schema.
type seededRows struct {
	repo map[string]string // format -> repository id
	rows map[string]string // table -> id of the one live row
}

// seedPre017 writes a raw, a maven and a docker repository with a few rows
// each, using only columns that exist before migration 017.
func seedPre017(t *testing.T, db *sql.DB) seededRows {
	t.Helper()

	const systemOrg = "00000000-0000-0000-0000-000000000001"

	s := seededRows{repo: map[string]string{}, rows: map[string]string{}}

	blob := mustScan[string](t, db,
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES (repeat('a', 64), 3, repeat('a', 64)) RETURNING id`)

	for _, format := range []string{"raw", "maven", "docker"} {
		s.repo[format] = mustScan[string](t, db,
			`INSERT INTO repositories (name, format, org_id) VALUES ($1, $1, $2) RETURNING id`, format, systemOrg)
	}

	s.rows["raw_artifacts"] = mustScan[string](t, db,
		`INSERT INTO raw_artifacts (repository_id, path, blob_id) VALUES ($1, 'a/b.txt', $2) RETURNING id`, s.repo["raw"], blob)
	mustExec(t, db, `INSERT INTO raw_artifacts (repository_id, path, blob_id) VALUES ($1, 'c.txt', $2)`, s.repo["raw"], blob)

	s.rows["maven_artifacts"] = mustScan[string](t, db,
		`INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id)
		 VALUES ($1, 'g', 'a', '1.0', 'jar', 'g/a/1.0/a-1.0.jar', $2) RETURNING id`, s.repo["maven"], blob)

	s.rows["docker_manifests"] = mustScan[string](t, db,
		`INSERT INTO docker_manifests (repository_id, digest, media_type, blob_id, image)
		 VALUES ($1, 'sha256:`+fmt.Sprintf("%064d", 1)+`', 'application/vnd.oci.image.manifest.v1+json', $2, 'app') RETURNING id`,
		s.repo["docker"], blob)
	s.rows["docker_tags"] = mustScan[string](t, db,
		`INSERT INTO docker_tags (repository_id, name, manifest_digest, image)
		 VALUES ($1, 'latest', 'sha256:`+fmt.Sprintf("%064d", 1)+`', 'app') RETURNING id`, s.repo["docker"])

	return s
}

// dupInsert is the statement copying the row with the given id ($1) of table,
// i.e. a second row with the same unique key.
func dupInsert(table, columns string) string {
	return fmt.Sprintf(`INSERT INTO %[1]s (%[2]s) SELECT %[2]s FROM %[1]s WHERE id = $1`, table, columns)
}

// assertTrashSchema checks the end state of migrations 017+018: every
// per-format table has the live-row unique index instead of the old
// constraint, no index is left invalid and the audit constraint is validated.
func assertTrashSchema(t *testing.T, db *sql.DB) {
	t.Helper()

	ctx := context.Background()

	if n := mustScan[int](t, db, `SELECT count(*) FROM pg_index WHERE NOT indisvalid`); n != 0 {
		t.Errorf("%d invalid indexes left behind", n)
	}

	if !mustScan[bool](t, db, `SELECT convalidated FROM pg_constraint WHERE conname = 'chk_audit_action'`) {
		t.Error("chk_audit_action is not validated")
	}

	rows, err := db.QueryContext(ctx,
		`SELECT table_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND column_name = 'deleted_at' AND table_name <> 'repositories'`)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var tables []string

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}

		tables = append(tables, name)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(tables) != 17 {
		t.Fatalf("%d tables with deleted_at, want 17: %v", len(tables), tables)
	}

	for _, table := range tables {
		if n := mustScan[int](t, db,
			`SELECT count(*) FROM pg_index WHERE indrelid = $1::regclass AND indisunique AND NOT indisprimary AND indpred IS NULL`, table); n != 0 {
			t.Errorf("%s: %d unique indexes covering trashed rows remain", table, n)
		}

		if n := mustScan[int](t, db,
			`SELECT count(*) FROM pg_constraint WHERE conrelid = $1::regclass AND contype = 'u'`, table); n != 0 {
			t.Errorf("%s: %d old unique constraints remain", table, n)
		}

		if n := mustScan[int](t, db,
			`SELECT count(*) FROM pg_index WHERE indrelid = $1::regclass AND indisunique AND indisvalid AND indpred IS NOT NULL`, table); n != 1 {
			t.Errorf("%s: %d live-row unique indexes, want 1", table, n)
		}

		if n := mustScan[int](t, db,
			`SELECT count(*) FROM pg_index WHERE indexrelid = to_regclass($1) AND indisvalid AND indpred IS NOT NULL AND NOT indisunique`, "idx_"+table+"_deleted"); n != 1 {
			t.Errorf("%s: deleted_at index missing or invalid", table)
		}
	}
}

// assertLiveRowUniqueness checks the unique semantics of the new indexes on
// the seeded rows: a duplicate of a live row is refused, a duplicate of a
// trashed row is accepted, and restoring the old copy conflicts with the new.
func assertLiveRowUniqueness(t *testing.T, db *sql.DB, s seededRows) {
	t.Helper()

	ctx := context.Background()

	for table, columns := range map[string]string{
		"raw_artifacts":    "repository_id, path, blob_id",
		"maven_artifacts":  "repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id",
		"docker_manifests": "repository_id, digest, media_type, blob_id, image",
		"docker_tags":      "repository_id, name, manifest_digest, image",
	} {
		id, insert := s.rows[table], dupInsert(table, columns)

		_, err := db.ExecContext(ctx, insert, id)
		wantPgCode(t, err, pgUniqueViolation, table+": duplicate of a live row")

		mustExec(t, db, `UPDATE `+table+` SET deleted_at = now(), deleted_by = 'test' WHERE id = $1`, id)

		if _, err := db.ExecContext(ctx, insert, id); err != nil {
			t.Errorf("%s: duplicate of a trashed row must be accepted: %v", table, err)
		}

		_, err = db.ExecContext(ctx, `UPDATE `+table+` SET deleted_at = NULL, deleted_by = NULL WHERE id = $1`, id)
		wantPgCode(t, err, pgUniqueViolation, table+": restore over a live copy")
	}
}

// assertAuditActions checks that the widened action list is enforced.
func assertAuditActions(t *testing.T, db *sql.DB) {
	t.Helper()

	mustExec(t, db, `INSERT INTO audit_logs (action) VALUES ('artifact.restored')`)

	_, err := db.ExecContext(context.Background(), `INSERT INTO audit_logs (action) VALUES ('not.an.action')`)
	wantPgCode(t, err, pgCheckViolation, "unknown audit action")
}

// Upgrading a database that holds data written by the previous release: the
// rows survive, uniqueness moves to live rows only, nothing is left invalid.
func TestTrashMigrationsUpgradeFrom016(t *testing.T) {
	db := scratchDB(t)

	migrateThrough(t, db, "016")

	s := seedPre017(t, db)

	migrateAll(t, db)

	for table, id := range s.rows {
		if n := mustScan[int](t, db, `SELECT count(*) FROM `+table+` WHERE id = $1 AND deleted_at IS NULL AND deleted_by IS NULL`, id); n != 1 {
			t.Errorf("%s: seeded row missing or altered after the upgrade", table)
		}
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM raw_artifacts`); n != 2 {
		t.Errorf("raw_artifacts holds %d rows, want 2", n)
	}

	if !mustScan[bool](t, db, `SELECT NOT immutable_releases AND deleted_at IS NULL AND trash_retention_days IS NULL FROM repositories WHERE id = $1`, s.repo["raw"]) {
		t.Error("existing repositories must default to no retention override and mutable releases")
	}

	for _, v := range []string{trashSchemaMigration, trashIndexesMigration} {
		if n := mustScan[int](t, db, `SELECT count(*) FROM schema_migrations WHERE version = $1`, v); n != 1 {
			t.Errorf("schema_migrations has %d rows for %s, want 1", n, v)
		}
	}

	assertTrashSchema(t, db)
	assertAuditActions(t, db)
	assertLiveRowUniqueness(t, db, s)

	// Re-applying is a no-op.
	migrateAll(t, db)
}

// The schema between 017 and 018, and in the middle of 018, keeps the old and
// the new code working, and a 018 that stopped half way continues on re-run.
func TestTrashMigrationsIntermediateStates(t *testing.T) {
	db := scratchDB(t)

	migrateThrough(t, db, "016")

	s := seedPre017(t, db)

	migrateThrough(t, db, "017")

	const upsert = `INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id)
		SELECT repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id FROM maven_artifacts WHERE id = $1
		ON CONFLICT (repository_id, repo_path) WHERE deleted_at IS NULL DO UPDATE SET extension = 'pom'`

	const oldUpsert = `INSERT INTO maven_artifacts (repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id)
		SELECT repository_id, group_id, artifact_id_coord, version, extension, repo_path, blob_id FROM maven_artifacts WHERE id = $1
		ON CONFLICT (repository_id, repo_path) DO UPDATE SET extension = 'pom'`

	mavenID := s.rows["maven_artifacts"]

	assertUpserted := func(stage, query string) {
		t.Helper()

		mustExec(t, db, `UPDATE maven_artifacts SET extension = 'jar' WHERE id = $1`, mavenID)
		mustExec(t, db, query, mavenID)

		if ext := mustScan[string](t, db, `SELECT extension FROM maven_artifacts WHERE id = $1`, mavenID); ext != "pom" {
			t.Errorf("%s: upsert did not update the conflicting row (extension %q)", stage, ext)
		}
	}

	// After 017: old constraint still in force, audit constraint added NOT VALID.
	if mustScan[bool](t, db, `SELECT convalidated FROM pg_constraint WHERE conname = 'chk_audit_action'`) {
		t.Error("017 must add chk_audit_action NOT VALID")
	}

	assertAuditActions(t, db)
	assertUpserted("after 017", upsert)
	assertUpserted("after 017, old arbiter", oldUpsert)

	// Run the start of 018 by hand and stop: unique index built, old constraint
	// still there.
	script, err := fs.ReadFile(migrations.FS, trashIndexesMigration)
	if err != nil {
		t.Fatal(err)
	}

	stmts := splitStatements(string(script))
	if len(stmts) < 4 {
		t.Fatalf("018 split into %d statements", len(stmts))
	}

	for _, stmt := range stmts[:2] { // invalid-index guard, maven unique index
		mustExec(t, db, stmt)
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM pg_constraint WHERE conname = 'maven_artifacts_repository_id_repo_path_key'`); n != 1 {
		t.Fatalf("old constraint should still exist after the unique index build, found %d", n)
	}

	assertUpserted("old constraint and live index side by side", upsert)

	// Drop the old constraint: the new arbiter keeps serving the new upsert, a
	// previous version's upsert without the predicate has none (documented).
	mustExec(t, db, stmts[2])
	assertUpserted("after dropping the old constraint", upsert)

	_, err = db.ExecContext(context.Background(), oldUpsert, mavenID)
	wantPgCode(t, err, pgNoArbiterIndex, "upsert without the live-row predicate once the constraint is gone")

	// 018 stopped here, unrecorded; the next start finishes it.
	migrateAll(t, db)

	assertTrashSchema(t, db)
	assertAuditActions(t, db)
	assertLiveRowUniqueness(t, db, s)
}

// A CREATE INDEX CONCURRENTLY that failed on an earlier start leaves an INVALID
// index which IF NOT EXISTS would keep; 018 replaces it.
func TestTrashMigrationsReplaceInvalidIndex(t *testing.T) {
	db := scratchDB(t)

	migrateThrough(t, db, "016")

	s := seedPre017(t, db)

	migrateThrough(t, db, "017")

	// Both seeded raw rows share a repository, so building this index over
	// repository_id alone fails and leaves it invalid.
	_, err := db.ExecContext(context.Background(),
		`CREATE UNIQUE INDEX CONCURRENTLY raw_artifacts_live_path_key ON raw_artifacts (repository_id) WHERE deleted_at IS NULL`)
	wantPgCode(t, err, pgUniqueViolation, "failed concurrent build")

	if mustScan[bool](t, db, `SELECT indisvalid FROM pg_index WHERE indexrelid = 'raw_artifacts_live_path_key'::regclass`) {
		t.Fatal("precondition: the failed build must leave an invalid index")
	}

	migrateAll(t, db)

	assertTrashSchema(t, db)

	def := mustScan[string](t, db, `SELECT indexdef FROM pg_indexes WHERE indexname = 'raw_artifacts_live_path_key'`)
	if want := `(repository_id, path) WHERE (deleted_at IS NULL)`; !strings.Contains(def, want) {
		t.Errorf("index definition %q does not contain %q", def, want)
	}

	assertLiveRowUniqueness(t, db, s)
}

// An empty database goes through the same 017 + 018 path.
func TestTrashMigrationsFreshInstall(t *testing.T) {
	db := scratchDB(t)

	migrateAll(t, db)

	assertTrashSchema(t, db)
	assertAuditActions(t, db)
}

// Dropping an old constraint needs ACCESS EXCLUSIVE for an instant; behind a
// long-running reader the migration gives up after 10 s instead of queueing
// every other query on the table, and a later start finishes the job.
func TestTrashMigrationsLockTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 10 s lock timeout")
	}

	ctx := context.Background()
	db := scratchDB(t)

	migrateThrough(t, db, "017")

	// A reader that keeps its ACCESS SHARE lock on the table the first drop targets.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `LOCK TABLE maven_artifacts IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = NewMigrator(db).Apply(ctx)

	wantPgCode(t, err, "55P03", "migration behind a long-running reader")

	if d := time.Since(start); d < 9*time.Second || d > 30*time.Second {
		t.Errorf("gave up after %v, want about 10 s", d)
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM schema_migrations WHERE version = $1`, trashIndexesMigration); n != 0 {
		t.Errorf("failed migration recorded %d times", n)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	migrateAll(t, db)

	assertTrashSchema(t, db)
}
