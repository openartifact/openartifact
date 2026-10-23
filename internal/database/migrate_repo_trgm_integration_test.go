// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import "testing"

const repoTrigramMigration = "040_repositories_name_trgm.no_tx.sql"

const trigramIndexState = `SELECT i.indisvalid FROM pg_index i
	JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = 'idx_repositories_name_trgm'`

// TestRepositoriesNameTrigramMigrationRerun: the migration builds a valid
// trigram index, and re-running it after an interrupted CREATE INDEX
// CONCURRENTLY (which leaves an INVALID index that IF NOT EXISTS would skip
// forever) replaces that index with a valid one.
func TestRepositoriesNameTrigramMigrationRerun(t *testing.T) {
	db := scratchDB(t)
	migrateAll(t, db)

	if !mustScan[bool](t, db, trigramIndexState) {
		t.Fatal("idx_repositories_name_trgm is not valid after the migrations")
	}

	mustExec(t, db, `UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'idx_repositories_name_trgm'::regclass`)
	mustExec(t, db, `DELETE FROM schema_migrations WHERE version = $1`, repoTrigramMigration)
	migrateAll(t, db)

	if !mustScan[bool](t, db, trigramIndexState) {
		t.Error("re-running the migration left the trigram index invalid")
	}

	// Applying it once more with the index healthy is a no-op.
	mustExec(t, db, `DELETE FROM schema_migrations WHERE version = $1`, repoTrigramMigration)
	migrateAll(t, db)

	if n := mustScan[int](t, db, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_repositories_name_trgm'`); n != 1 {
		t.Errorf("%d trigram indexes after a repeated run, want 1", n)
	}
}
