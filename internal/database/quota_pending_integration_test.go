// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
)

func pendingEntries(tb testing.TB, pg *PostgresDB, repoID string) int {
	tb.Helper()

	var n int
	if err := pg.GetDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM quota_pending WHERE repository_id = $1`, repoID).Scan(&n); err != nil {
		tb.Fatalf("count pending entries: %v", err)
	}

	return n
}

func reserveN(tb testing.TB, pg *PostgresDB, repoID string, size int64, count int) {
	tb.Helper()

	ok, err := pg.ReserveStorageUsage(context.Background(), repoID, size, count)
	if err != nil || !ok {
		tb.Fatalf("ReserveStorageUsage(%d, %d) = %v, %v; want accepted", size, count, ok, err)
	}
}

func releaseN(tb testing.TB, pg *PostgresDB, repoID string, size int64, count int) {
	tb.Helper()

	if err := pg.DecrementStorageUsage(context.Background(), repoID, size, count); err != nil {
		tb.Fatalf("DecrementStorageUsage(%d, %d): %v", size, count, err)
	}
}

func mustReconcile(tb testing.TB, pg *PostgresDB, repoID string) {
	tb.Helper()

	if _, err := pg.ReconcileStorageUsage(context.Background(), repoID); err != nil {
		tb.Fatalf("ReconcileStorageUsage: %v", err)
	}
}

// A reconcile between the reservation and the row insert must neither drop the
// in-flight upload from the counter nor count it twice once the row lands.
func TestReconcileCountsUploadInFlight(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	s := newQuotaSeeder(t, pg, "inflight", "helm")

	s.helm(s.blob(10))
	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 10, 1)

	reserveN(t, pg, s.repo, 25, 1)
	requireUsage(t, pg, s.repo, 35, 2)

	mustReconcile(t, pg, s.repo) // the upload has reserved but not inserted its row yet
	requireUsage(t, pg, s.repo, 35, 2)

	s.helm(s.blob(25)) // the row lands: the reservation is now backed by it
	requireUsage(t, pg, s.repo, 35, 2)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Fatalf("%d pending entries left after the row was inserted, want 0", n)
	}

	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 35, 2)
}

// An upload that fails gives its reservation back together with its entry.
func TestReleaseConsumesPendingEntry(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	s := newQuotaSeeder(t, pg, "failed-upload", "helm")

	reserveN(t, pg, s.repo, 40, 1)
	releaseN(t, pg, s.repo, 40, 1)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Fatalf("%d pending entries after the release, want 0", n)
	}

	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 0, 0)
}

// A reconcile between the row delete and the release must keep the usage the
// release is still going to return, instead of returning it twice.
func TestReconcileCountsDeleteInFlight(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	s := newQuotaSeeder(t, pg, "delete-inflight", "helm")

	s.helm(s.blob(30))
	s.helm(s.blob(5))
	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 35, 2)

	s.exec(`DELETE FROM helm_charts WHERE repository_id = $1 AND name = 'chart-1'`, s.repo)

	mustReconcile(t, pg, s.repo) // row gone, quota not released yet
	requireUsage(t, pg, s.repo, 35, 2)

	releaseN(t, pg, s.repo, 30, 1)
	requireUsage(t, pg, s.repo, 5, 1)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Fatalf("%d pending entries after the release, want 0", n)
	}

	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 5, 1)
}

// Replacing a package's blob reserves the new usage and returns the old one.
func TestReconcileCountsReplaceInFlight(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	s := newQuotaSeeder(t, pg, "replace", "helm")

	s.helm(s.blob(10))
	mustReconcile(t, pg, s.repo)

	reserveN(t, pg, s.repo, 15, 1)
	s.exec(`UPDATE helm_charts SET blob_id = $2 WHERE repository_id = $1`, s.repo, s.blob(15))

	mustReconcile(t, pg, s.repo) // new row content in place, the old usage not returned yet
	requireUsage(t, pg, s.repo, 25, 2)

	releaseN(t, pg, s.repo, 10, 1)
	requireUsage(t, pg, s.repo, 15, 1)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Fatalf("%d pending entries, want 0", n)
	}
}

// A batch release (cleanup of many rows) consumes the entries of every row.
func TestReleaseBatchConsumesPendingEntries(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	s := newQuotaSeeder(t, pg, "batch", "helm")

	for _, size := range []int64{10, 20, 30} {
		s.helm(s.blob(size))
	}

	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 60, 3)

	s.exec(`DELETE FROM helm_charts WHERE repository_id = $1`, s.repo)

	if n := pendingEntries(t, pg, s.repo); n != 3 {
		t.Fatalf("%d pending entries after deleting 3 rows, want 3", n)
	}

	releaseN(t, pg, s.repo, 60, 3)
	requireUsage(t, pg, s.repo, 0, 0)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Fatalf("%d pending entries after the batch release, want 0", n)
	}
}

// An entry whose partner never came (a crashed upload) stops counting, so the
// reconcile still heals the counter it left behind.
func TestReconcileDropsAbandonedEntries(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	s := newQuotaSeeder(t, pg, "abandoned", "helm")

	reserveN(t, pg, s.repo, 50, 1)
	s.exec(`UPDATE quota_pending SET created_at = now() - interval '2 hours' WHERE repository_id = $1`, s.repo)

	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 0, 0)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Fatalf("%d stale entries kept, want 0", n)
	}

	releaseN(t, pg, s.repo, 50, 1) // the late release finds nothing to consume and clamps at zero
	requireUsage(t, pg, s.repo, 0, 0)
}

// A reservation committed after the reconcile's snapshot is not overwritten:
// the fix applies a difference, not the absolute recount.
func TestReconcileKeepsConcurrentReservation(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "concurrent", "helm")

	s.helm(s.blob(10))
	mustReconcile(t, pg, s.repo)

	// A reservation transaction holds the counter row; the reconcile takes its
	// snapshot, then waits for that row.
	tx, err := pg.GetDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM storage_quotas WHERE repository_id = $1 FOR UPDATE`, s.repo); err != nil {
		t.Fatalf("lock counter: %v", err)
	}

	done := make(chan error, 1)

	go func() {
		_, err := pg.ReconcileStorageUsage(ctx, s.repo)
		done <- err
	}()

	// Wait until the reconcile is blocked on the counter row, snapshot taken.
	for {
		var waiting bool
		if err := pg.GetDB().QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database())`).Scan(&waiting); err != nil {
			t.Fatalf("poll lock waits: %v", err)
		}

		if waiting {
			break
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE storage_quotas SET current_size_bytes = current_size_bytes + 7, current_artifact_count = current_artifact_count + 1 WHERE repository_id = $1`, s.repo); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO quota_pending (repository_id, size_bytes, artifact_count) VALUES ($1, 7, 1)`, s.repo); err != nil {
		t.Fatalf("pending entry: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("ReconcileStorageUsage: %v", err)
	}

	requireUsage(t, pg, s.repo, 17, 2)
}

// A Go module is one artifact reserved as zip plus go.mod; its row consumes
// that single entry.
func TestGomodRowConsumesOneEntry(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	s := newQuotaSeeder(t, pg, "gomod", "go")

	reserveN(t, pg, s.repo, 100+7, 1)

	zip, mod := s.blob(100), s.blob(7)
	s.exec(`INSERT INTO gomod_modules (repository_id, module_path, version, go_mod_content, zip_blob_id, mod_blob_id)
	        VALUES ($1, 'example.com/m', 'v1.0.0', 'module example.com/m', $2, $3)`, s.repo, zip, mod)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Fatalf("%d pending entries after the module row, want 0", n)
	}

	mustReconcile(t, pg, s.repo)
	requireUsage(t, pg, s.repo, 107, 1)
}
