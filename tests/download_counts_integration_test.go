// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
)

// downloads reads download_count of a package file straight from the table.
func downloads(t *testing.T, pg *database.PostgresDB, fileID string) int64 {
	t.Helper()

	var n int64

	err := pg.GetDB().QueryRowContext(context.Background(), `SELECT download_count FROM package_files WHERE id = $1`, fileID).Scan(&n)
	if err != nil {
		t.Fatalf("read download_count of %s: %v", fileID, err)
	}

	return n
}

// crateFileID seeds a crate and returns the id of its file.
func crateFileID(t *testing.T, pg *database.PostgresDB, repoID, name string) string {
	t.Helper()

	seedCargoCrate(t, pg, repoID, name, "1.0.0", "", false, 0)

	crate, err := pg.GetCargoCrate(context.Background(), repoID, name, "1.0.0")
	if err != nil || crate == nil {
		t.Fatalf("GetCargoCrate(%s) = %v, %v", name, crate, err)
	}

	return crate.ID
}

// Counting a download costs no write; after a flush the counter holds exactly
// the downloads that were counted, however many goroutines counted them.
func TestDownloadCountsConvergeExactlyAfterFlush(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "crates", "cargo")

	hot := crateFileID(t, pg, repoID, "hot")
	cold := crateFileID(t, pg, repoID, "cold")

	const workers, perWorker = 16, 250

	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			for range perWorker {
				pg.CountFileDownload(hot)
			}

			pg.CountFileDownload(cold)
		})
	}

	wg.Wait()

	if got := downloads(t, pg, hot); got != 0 {
		t.Fatalf("download_count before the flush = %d, want 0 (counts are batched)", got)
	}

	if err := pg.FlushDownloadCounts(ctx); err != nil {
		t.Fatalf("FlushDownloadCounts: %v", err)
	}

	if got := downloads(t, pg, hot); got != workers*perWorker {
		t.Errorf("hot download_count = %d, want %d", got, workers*perWorker)
	}

	if got := downloads(t, pg, cold); got != workers {
		t.Errorf("cold download_count = %d, want %d", got, workers)
	}

	// A second flush with nothing pending changes nothing; a later download adds on.
	if err := pg.FlushDownloadCounts(ctx); err != nil {
		t.Fatal(err)
	}

	pg.CountFileDownload(hot)

	if err := pg.FlushDownloadCounts(ctx); err != nil {
		t.Fatal(err)
	}

	if got := downloads(t, pg, hot); got != workers*perWorker+1 {
		t.Errorf("hot download_count after one more download = %d, want %d", got, workers*perWorker+1)
	}
}

// The periodic flusher writes the counts without anyone asking.
func TestDownloadCountsAreFlushedPeriodically(t *testing.T) {
	pg := freshPostgresCfg(t, 5, func(c *database.DatabaseConfig) { c.DownloadCountFlushInterval = 50 * time.Millisecond })
	repoID := newRepo(t, pg, "crates", "cargo")
	id := crateFileID(t, pg, repoID, "tick")

	pg.CountFileDownload(id)
	pg.CountFileDownload(id)

	deadline := time.Now().Add(10 * time.Second)
	for downloads(t, pg, id) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("download_count = %d after 10s, want 2: the periodic flush did not run", downloads(t, pg, id))
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// Closing the database (server shutdown) writes the counts still pending.
func TestDownloadCountsAreFlushedOnClose(t *testing.T) {
	pg := freshPostgres(t, 5)
	repoID := newRepo(t, pg, "crates", "cargo")
	id := crateFileID(t, pg, repoID, "shutdown")

	// The replica under test has a flush interval far beyond the test, so only
	// Close can write.
	replica := secondPostgres(t, pg, func(c *database.DatabaseConfig) { c.DownloadCountFlushInterval = time.Hour })

	for range 7 {
		replica.CountFileDownload(id)
	}

	if got := downloads(t, pg, id); got != 0 {
		t.Fatalf("download_count before Close = %d, want 0", got)
	}

	if err := replica.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := downloads(t, pg, id); got != 7 {
		t.Errorf("download_count after Close = %d, want 7", got)
	}
}

// Replicas count independently and their deltas add up, including when both
// flush overlapping sets of files at the same time.
func TestDownloadCountsAddUpAcrossReplicas(t *testing.T) {
	a := freshPostgres(t, 5)
	b := secondPostgres(t, a, nil)
	ctx := context.Background()
	repoID := newRepo(t, a, "crates", "cargo")

	const files, rounds = 40, 25

	ids := make([]string, files)
	for i := range ids {
		ids[i] = crateFileID(t, a, repoID, "crate-"+string(rune('a'+i/26))+string(rune('a'+i%26)))
	}

	var wg sync.WaitGroup

	for _, replica := range []*database.PostgresDB{a, b} {
		wg.Go(func() {
			for range rounds {
				for _, id := range ids {
					replica.CountFileDownload(id)
				}

				if err := replica.FlushDownloadCounts(ctx); err != nil {
					t.Errorf("FlushDownloadCounts: %v", err)
					return
				}
			}
		})
	}

	wg.Wait()

	for _, id := range ids {
		if got := downloads(t, a, id); got != 2*rounds {
			t.Fatalf("download_count of %s = %d, want %d (both replicas counted %d)", id, got, 2*rounds, rounds)
		}
	}
}

// A counted download of a file that was trashed meanwhile is skipped, and
// counting never touches updated_at.
func TestDownloadCountsLeaveUpdatedAtAndSkipTrashedFiles(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()
	repoID := newRepo(t, pg, "crates", "cargo")
	live := crateFileID(t, pg, repoID, "live")
	trashed := crateFileID(t, pg, repoID, "trashed")

	var before time.Time
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT updated_at FROM package_files WHERE id = $1`, live).Scan(&before); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE package_files SET deleted_at = now() WHERE id = $1`, trashed); err != nil {
		t.Fatal(err)
	}

	pg.CountFileDownload(live)
	pg.CountFileDownload(trashed)

	if err := pg.FlushDownloadCounts(ctx); err != nil {
		t.Fatal(err)
	}

	var after time.Time
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT updated_at FROM package_files WHERE id = $1`, live).Scan(&after); err != nil {
		t.Fatal(err)
	}

	if !after.Equal(before) {
		t.Errorf("updated_at moved from %v to %v: a download is not a modification", before, after)
	}

	if got := downloads(t, pg, live); got != 1 {
		t.Errorf("live download_count = %d, want 1", got)
	}

	if got := downloads(t, pg, trashed); got != 0 {
		t.Errorf("trashed download_count = %d, want 0", got)
	}
}
