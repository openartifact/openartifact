// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
	"time"
)

// settleAgainstUpload returns once the operation running in the background has
// either finished (it did not wait for the uploader, so it decided on a
// snapshot that cannot see the upload) or is waiting for a lock of the test
// database (it waits for the uploader to commit). It returns whether the
// operation finished, and its result when it did.
func settleAgainstUpload(t *testing.T, pg *PostgresDB, done <-chan error) (bool, error) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return true, err
		default:
		}

		if countRows(t, pg, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`) > 0 {
			return false, nil
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("the operation neither finished nor waited for the uploader's lock")

	return false, nil
}

// startUpload writes file b into the live version of the package a was written
// to, in a transaction left open: the uploader's rows are uncommitted, which is
// all a concurrent delete can see of them.
func startUpload(t *testing.T, pg *PostgresDB, repo, b string) func() {
	t.Helper()

	ctx := context.Background()

	tx, err := pg.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin upload: %v", err)
	}

	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	_, err = pg.writePackageTx(ctx, pg.q.WithTx(tx), &PackageWrite{
		RepoID: repo, Format: "maven", Name: "org.acme:lib", NameKey: "org.acme:lib",
		Version: "1.0", VersionKey: "1.0",
		Files: []FileWrite{{Path: b, Filename: b}},
	}, []*string{nil}, []byte(`{}`), []byte(`{}`))
	if err != nil {
		t.Fatalf("write upload: %v", err)
	}

	return func() {
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit upload: %v", err)
		}
	}
}

func seedFirstFile(t *testing.T, pg *PostgresDB, repo, filename string) string {
	t.Helper()

	return seedPackageFile(t, pg, packageSeed{
		repo: repo, format: "maven", name: "org.acme:lib", version: "1.0", filename: filename,
	})
}

// TestDeleteDoesNotPruneVersionOfConcurrentUpload: deleting the last committed
// file of a version while another upload adds a file to the same version must
// leave the version and the uploaded file alone. The emptiness check cannot see
// the uncommitted file; the prune has to wait for the uploader and then look.
func TestDeleteDoesNotPruneVersionOfConcurrentUpload(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	repo := seedRepo(t, pg.GetDB(), "prune-race", "maven")
	a := seedFirstFile(t, pg, repo, "lib-1.0.jar")
	commit := startUpload(t, pg, repo, "lib-1.0-sources.jar")

	done := make(chan error, 1)

	go func() {
		_, err := pg.deleteFiles(context.Background(), repo, []string{a})
		done <- err
	}()

	finished, opErr := settleAgainstUpload(t, pg, done)

	commit()

	if !finished {
		opErr = <-done
	}

	if opErr != nil {
		t.Fatalf("deleteFiles: %v", opErr)
	}

	if n := countRows(t, pg, `SELECT count(*) FROM package_files WHERE repository_id = $1 AND path = 'lib-1.0-sources.jar'`, repo); n != 1 {
		t.Errorf("uploaded file rows = %d, want 1: the prune cascaded it away", n)
	}

	if n := countRows(t, pg, `SELECT count(*) FROM package_versions WHERE repository_id = $1`, repo); n != 1 {
		t.Errorf("version rows = %d, want 1", n)
	}

	if n := countRows(t, pg, `SELECT count(*) FROM packages WHERE repository_id = $1`, repo); n != 1 {
		t.Errorf("package rows = %d, want 1", n)
	}
}

// TestTrashDoesNotStampVersionOfConcurrentUpload is the soft-delete variant:
// the version must not be trashed under a live file the uploader commits.
func TestTrashDoesNotStampVersionOfConcurrentUpload(t *testing.T) {
	registerFixtures(t)

	pg := newQuotaTestDB(t, 8)
	repo := seedRepo(t, pg.GetDB(), "trash-race", "maven")
	a := seedFirstFile(t, pg, repo, "lib-1.0.jar")
	commit := startUpload(t, pg, repo, "lib-1.0-sources.jar")

	done := make(chan error, 1)

	go func() {
		ctx := context.Background()

		tx, err := pg.GetDB().BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}

		defer func() { _ = tx.Rollback() }()

		if err := TrashFilesInTx(ctx, tx, repo, []string{a}, "admin"); err != nil {
			done <- err
			return
		}

		done <- tx.Commit()
	}()

	finished, opErr := settleAgainstUpload(t, pg, done)

	commit()

	if !finished {
		opErr = <-done
	}

	if opErr != nil {
		t.Fatalf("TrashFilesInTx: %v", opErr)
	}

	if n := countRows(t, pg, `SELECT count(*) FROM package_versions WHERE repository_id = $1 AND deleted_at IS NOT NULL`, repo); n != 0 {
		t.Errorf("trashed versions = %d, want 0: the version was stamped under the uploaded live file", n)
	}

	if n := countRows(
		t,
		pg,
		`SELECT count(*) FROM package_files WHERE repository_id = $1 AND path = 'lib-1.0-sources.jar' AND deleted_at IS NULL`,
		repo,
	); n != 1 {
		t.Errorf("live uploaded files = %d, want 1", n)
	}
}
