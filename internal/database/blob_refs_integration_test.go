// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Generated-metadata and delta blobs share the content-addressed key space with
// ordinary blobs but have no blobs row. A blobs row created for byte-identical
// content (a raw upload of the same bytes) must not let its deletion take the
// storage object out from under them, and the check must also run when there
// is no blobs row at all.
func TestBlobReferencesCoverMetadataAndDeltas(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "blob-refs", "raw")

	metaSHA := strings.Repeat("a", 64)
	deltaSHA := strings.Repeat("b", 64)
	bareSHA := strings.Repeat("c", 64)

	s.exec(
		`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, 'maven-metadata.xml', $2, 1)`,
		s.repo,
		metaSHA,
	)
	s.exec(`INSERT INTO package_deltas (repository_id, from_sha256, from_filename, to_sha256, to_filename, delta_sha256, delta_size, format)
	        VALUES ($1, $2, 'a', $3, 'b', $4, 1, 'rpm')`, s.repo, strings.Repeat("d", 64), strings.Repeat("e", 64), deltaSHA)

	// A raw upload of the same bytes leaves a blobs row with no package row (after its delete).
	for _, sha := range []string{metaSHA, deltaSHA, bareSHA} {
		s.exec(
			`INSERT INTO blobs (sha256, size_bytes, blob_key, created_at) VALUES ($1::varchar, 1, $1::text, now() - interval '2 hours')`,
			sha,
		)
	}

	for name, sha := range map[string]string{"metadata": metaSHA, "delta": deltaSHA} {
		n, err := pg.BlobReferenceCount(ctx, sha)
		if err != nil || n != 1 {
			t.Fatalf("%s: BlobReferenceCount = %d, %v; want 1", name, n, err)
		}

		removed, err := pg.DeleteBlobIfUnreferenced(ctx, sha)
		if err != nil || removed {
			t.Fatalf("%s: DeleteBlobIfUnreferenced = %v, %v; want kept", name, removed, err)
		}
	}

	if n, err := pg.CountBlobDeletions(ctx); err != nil || n != 0 {
		t.Fatalf("referenced blobs queued %d deletions (err %v), want 0", n, err)
	}

	// Only the blob nothing points at is an orphan.
	orphans, _, err := pg.ListUnreferencedBlobs(ctx, time.Hour, "", 100, 100)
	if err != nil {
		t.Fatalf("ListUnreferencedBlobs: %v", err)
	}

	if len(orphans) != 1 || orphans[0] != bareSHA {
		t.Fatalf("orphans = %v, want only %s", orphans, bareSHA)
	}

	// Without a blobs row the references still count, and a bare sha is deletable.
	s.exec(`DELETE FROM blobs WHERE sha256 = $1`, metaSHA)

	if n, err := pg.BlobReferenceCount(ctx, metaSHA); err != nil || n != 1 {
		t.Fatalf("no blobs row: BlobReferenceCount = %d, %v; want 1", n, err)
	}

	ghost := strings.Repeat("f", 64)
	removed, err := pg.DeleteBlobIfUnreferenced(ctx, ghost)
	if err != nil || !removed {
		t.Fatalf("unreferenced sha without a blobs row: removed=%v err=%v; want removed", removed, err)
	}

	if n, err := pg.CountBlobDeletions(ctx); err != nil || n != 1 {
		t.Fatalf("queued deletions = %d (err %v), want the ghost's bytes queued", n, err)
	}
}

// A package file references its blob while live and while it sits in the trash
// (the purge frees the bytes); ArtifactExists only counts live files of the
// repository, whatever the format.
func TestBlobReferencesCountPackageFiles(t *testing.T) {
	registerFixtures(t)

	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "blob-refs-files", "helm")
	other := newQuotaSeeder(t, pg, "blob-refs-other", "helm")

	blob := s.blob(11)
	s.helm(blob)

	sha := s.queryID(`SELECT sha256 FROM blobs WHERE id = $1`, blob)

	if n, err := pg.BlobReferenceCount(ctx, sha); err != nil || n != 1 {
		t.Fatalf("live file: BlobReferenceCount = %d, %v; want 1", n, err)
	}

	if ok, err := pg.ArtifactExists(ctx, sha, s.repo); err != nil || !ok {
		t.Fatalf("ArtifactExists(own repo) = %v, %v; want true", ok, err)
	}

	if ok, err := pg.ArtifactExists(ctx, sha, other.repo); err != nil || ok {
		t.Fatalf("ArtifactExists(other repo) = %v, %v; want false", ok, err)
	}

	id := s.queryID(`SELECT id FROM package_files WHERE repository_id = $1`, s.repo)
	if soft, err := pg.TrashRows(ctx, s.repo, "helm_chart", []string{id}, "x"); err != nil || !soft {
		t.Fatalf("TrashRows = %v, %v", soft, err)
	}

	if n, err := pg.BlobReferenceCount(ctx, sha); err != nil || n != 1 {
		t.Fatalf("trashed file: BlobReferenceCount = %d, %v; want 1 (the purge frees it)", n, err)
	}

	if ok, err := pg.ArtifactExists(ctx, sha, s.repo); err != nil || ok {
		t.Fatalf("ArtifactExists(trashed) = %v, %v; want false", ok, err)
	}

	if _, err := pg.PurgeTrash(ctx, "helm_chart", id); err != nil {
		t.Fatal(err)
	}

	if n, err := pg.BlobReferenceCount(ctx, sha); err != nil || n != 0 {
		t.Fatalf("purged file: BlobReferenceCount = %d, %v; want 0", n, err)
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// When the session behind a blob lock dies while the holder runs (a failover, a
// dropped connection, pg_terminate_backend), the advisory lock is gone and fn ran
// without the exclusion it relied on: WithBlobLocks must say so instead of
// returning nil.
func TestWithBlobLocksReportsALostLock(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	sha := strings.Repeat("a", 64)

	ran := false

	err := pg.WithBlobLock(ctx, sha, func(ctx context.Context) error {
		ran = true

		var pid int
		if err := pg.GetDB().QueryRowContext(ctx,
			`SELECT pid FROM pg_stat_activity
			 WHERE datname = current_database() AND state = 'idle in transaction' AND query LIKE '%pg_advisory_xact_lock%'`,
		).Scan(&pid); err != nil {
			return fmt.Errorf("find the lock session: %w", err)
		}

		if _, err := pg.GetDB().ExecContext(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
			return fmt.Errorf("terminate the lock session: %w", err)
		}

		// Wait until the backend is really gone, so the outcome does not depend
		// on whether the signal beats the commit.
		waitFor(t, "the lock session to end", func() bool {
			var n int
			if err := pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&n); err != nil {
				return false
			}

			return n == 0
		})

		return nil
	})
	if !ran {
		t.Fatalf("fn did not run: %v", err)
	}

	if !errors.Is(err, ErrBlobLockLost) {
		t.Fatalf("WithBlobLock = %v; want ErrBlobLockLost after the lock session was killed", err)
	}
}

// A server that sets idle_in_transaction_session_timeout would kill a lock
// transaction (idle while its holder works on other connections) and drop the
// lock halfway; the lock transactions switch the timeout off for themselves.
func TestBlobLockSurvivesIdleInTransactionTimeout(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()

	var dbName string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.GetDB().ExecContext(ctx,
		fmt.Sprintf(`ALTER DATABASE %s SET idle_in_transaction_session_timeout = '200ms'`, dbName)); err != nil {
		t.Fatalf("set the timeout: %v", err)
	}

	// Sessions opened from now on carry the timeout; drop the idle ones.
	pg.GetDB().SetMaxIdleConns(0)

	// Control: an ordinary transaction left idle is killed by the server.
	tx, err := pg.GetDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}

	// Every statement restarts the idle clock, so leave the transaction alone
	// for longer than the timeout before probing it.
	idleFor := 600 * time.Millisecond // three times the timeout

	time.Sleep(idleFor)

	if _, err := tx.ExecContext(ctx, `SELECT 1`); err == nil {
		t.Fatal("the idle control transaction survived; the timeout is not in effect, the test proves nothing")
	}

	err = pg.WithBlobLock(ctx, strings.Repeat("b", 64), func(ctx context.Context) error {
		time.Sleep(idleFor)

		return nil
	})
	if err != nil {
		t.Fatalf("WithBlobLock held %v under a 200ms idle timeout: %v", idleFor, err)
	}
}
