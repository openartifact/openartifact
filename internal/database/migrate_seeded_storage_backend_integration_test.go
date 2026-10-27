// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"strings"
	"testing"
)

const (
	seededStorageBackendPrevious  = "086"
	seededStorageBackendMigration = "087_drop_seeded_storage_backend.sql"
)

const seededBackendCount = `SELECT count(*) FROM storage_backends`

// TestSeededStorageBackendMigration: 087 removes the row 001 seeded only while
// it is still untouched and unused; any edit or reference keeps it.
func TestSeededStorageBackendMigration(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	tests := []struct {
		name     string
		prepare  string // run against the pre-087 schema, with $seed = the seed id
		wantKept bool
	}{
		{name: "untouched seed is removed", prepare: ``},
		{
			name:    "a status written by the old test button does not count as an edit",
			prepare: `UPDATE storage_backends SET status = 'unreachable', status_message = 'path missing', last_checked_at = now()`,
		},
		{name: "renamed seed is kept", prepare: `UPDATE storage_backends SET name = 'archive'`, wantKept: true},
		{name: "re-pointed seed is kept", prepare: `UPDATE storage_backends SET config = '{"path": "/data/cold"}'`, wantKept: true},
		{name: "seed with extra config is kept", prepare: `UPDATE storage_backends SET config = '{"path": "./storage", "x": 1}'`, wantKept: true},
		{name: "seed of another kind is kept", prepare: `UPDATE storage_backends SET kind = 's3', config = '{"bucket": "b"}'`, wantKept: true},
		{
			name:     "seed that serves a tier is kept",
			prepare:  `INSERT INTO storage_tiers (name, backend_id, rank) SELECT 'cold', id, 1 FROM storage_backends`,
			wantKept: true,
		},
		{
			name: "seed that holds a blob is kept",
			prepare: `INSERT INTO blobs (sha256, size_bytes, blob_key, storage_backend_id)
				SELECT '` + sha + `', 1, 'k', id FROM storage_backends`,
			wantKept: true,
		},
		{
			name: "seed named by a tier move is kept",
			prepare: `INSERT INTO storage_tier_moves (sha256, from_backend_id, state)
				SELECT '` + sha + `', id, 'copying' FROM storage_backends`,
			wantKept: true,
		},
		{
			name: "seed with a queued copy deletion is kept",
			prepare: `INSERT INTO blob_deletions (sha256, scope, not_before)
				SELECT '` + sha + `', id::text, now() FROM storage_backends`,
			wantKept: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := scratchDB(t)

			migrateThrough(t, db, seededStorageBackendPrevious)

			if n := mustScan[int](t, db, seededBackendCount); n != 1 {
				t.Fatalf("schema through 086 holds %d storage backends, want the 1 seeded by 001", n)
			}

			if strings.TrimSpace(tt.prepare) != "" {
				mustExec(t, db, tt.prepare)
			}

			migrateAll(t, db)

			want := 0
			if tt.wantKept {
				want = 1
			}

			if n := mustScan[int](t, db, seededBackendCount); n != want {
				t.Errorf("storage_backends holds %d rows after 087, want %d", n, want)
			}

			if n := mustScan[int](t, db, `SELECT count(*) FROM schema_migrations WHERE version = $1`, seededStorageBackendMigration); n != 1 {
				t.Errorf("schema_migrations has %d rows for %s, want 1", n, seededStorageBackendMigration)
			}
		})
	}
}

// TestSeededStorageBackendMigrationFreshInstall: a new database ends up with no
// storage backend, and the primary tier is untouched.
func TestSeededStorageBackendMigrationFreshInstall(t *testing.T) {
	db := scratchDB(t)

	migrateAll(t, db)

	if n := mustScan[int](t, db, seededBackendCount); n != 0 {
		t.Errorf("a fresh install holds %d storage backends, want 0", n)
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM storage_tiers WHERE backend_id IS NULL`); n != 1 {
		t.Errorf("a fresh install holds %d primary tiers, want 1", n)
	}
}
