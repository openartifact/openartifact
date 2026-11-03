// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedOldBlob inserts a blobs row older than the orphan grace and returns its sha256.
func seedOldBlob(t *testing.T, s *quotaSeeder, n int) string {
	t.Helper()

	sha := fmt.Sprintf("%064x", n)
	s.exec(`INSERT INTO blobs (sha256, size_bytes, blob_key, created_at)
	        VALUES ($1::varchar, 1, $1::text, now() - interval '3 hours')`, sha)

	return sha
}

func blobExists(t *testing.T, s *quotaSeeder, sha string) bool {
	t.Helper()

	return s.queryID(`SELECT count(*)::text FROM blobs WHERE sha256 = $1`, sha) == "1"
}

// DeleteBlobsIfUnreferenced removes only the blobs no table references, queues
// their bytes, and leaves referenced ones (package file, generated metadata) alone.
func TestDeleteBlobsIfUnreferenced(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "gc-batch", "helm")

	fileBlob := s.blob(5)
	fileSHA := s.blobs[fileBlob].SHA256
	s.helm(fileBlob)

	metaSHA := seedOldBlob(t, s, 1)
	s.exec(
		`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, 'index.yaml', $2, 1)`,
		s.repo, metaSHA,
	)

	orphans := []string{seedOldBlob(t, s, 2), seedOldBlob(t, s, 3)}
	ghost := strings.Repeat("f", 64) // no blobs row at all

	removed, err := pg.DeleteBlobsIfUnreferenced(ctx, []string{fileSHA, metaSHA, ghost, orphans[0], orphans[1], orphans[0]})
	if err != nil {
		t.Fatalf("DeleteBlobsIfUnreferenced: %v", err)
	}

	if removed != 2 {
		t.Errorf("removed = %d, want the 2 blobs rows without a reference", removed)
	}

	for _, sha := range []string{fileSHA, metaSHA} {
		if !blobExists(t, s, sha) {
			t.Errorf("referenced blob %s was deleted", sha)
		}
	}

	for _, sha := range orphans {
		if blobExists(t, s, sha) {
			t.Errorf("orphan %s survived", sha)
		}
	}

	if n, err := pg.CountBlobDeletions(ctx); err != nil || n != 2 {
		t.Errorf("queued deletions = %d (err %v), want 2", n, err)
	}

	if n, err := pg.DeleteBlobsIfUnreferenced(ctx, nil); err != nil || n != 0 {
		t.Errorf("empty batch = %d, %v", n, err)
	}
}

// Walking ListUnreferencedBlobs window by window finds every orphan exactly
// once and ends with the wrapped cursor.
func TestListUnreferencedBlobsKeysetWindows(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "gc-window", "helm")

	var want []string

	for i := 1; i <= 25; i++ {
		want = append(want, seedOldBlob(t, s, i))
	}

	// A recent blob and a referenced one are never listed.
	recent := fmt.Sprintf("%064x", 900)
	s.exec(`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1::varchar, 1, $1::text)`, recent)

	refBlob := s.blob(3)
	s.helm(refBlob)

	var got []string

	after := ""

	for range 20 {
		shas, next, err := pg.ListUnreferencedBlobs(ctx, time.Hour, after, 10, 100)
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, shas...)

		if next == "" {
			break
		}

		after = next
	}

	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}

	// The limit cuts a window short: the cursor resumes right after the last hit.
	shas, next, err := pg.ListUnreferencedBlobs(ctx, time.Hour, "", 1000, 3)
	if err != nil || len(shas) != 3 || next != shas[2] {
		t.Fatalf("limited listing = %v, next %q, err %v; want 3 hits resuming after the last", shas, next, err)
	}
}

// An uploader that holds a blob's lock while it creates the reference never
// loses the blob to a concurrent batch delete: the delete takes the same lock
// and re-checks after it.
func TestDeleteBlobsIfUnreferencedNeverTakesAReferencedBlob(t *testing.T) {
	pg := newQuotaTestDB(t, 16)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "gc-race", "helm")

	const n = 60

	shas := make([]string, n)
	for i := range n {
		shas[i] = seedOldBlob(t, s, 100+i)
	}

	referenced := make([]bool, n)

	var wg sync.WaitGroup

	errs := make(chan error, n+8)

	// Uploaders reference half of the blobs, each under the blob lock, the way
	// an upload does (blob row ensured, reference inserted, lock released).
	for i := 0; i < n; i += 2 {
		referenced[i] = true

		wg.Go(func() {
			err := pg.WithBlobLock(ctx, shas[i], func(ctx context.Context) error {
				id, err := pg.UpsertBlob(ctx, shas[i], shas[i], 1)
				if err != nil {
					return err
				}

				time.Sleep(5 * time.Millisecond)

				_, err = pg.GetDB().ExecContext(ctx, `
					WITH p AS (INSERT INTO packages (repository_id, format, name, name_key) VALUES ($1, 'helm', $2, $2) RETURNING id),
					v AS (INSERT INTO package_versions (package_id, repository_id, version, version_key) SELECT id, $1, '1', '1' FROM p RETURNING id)
					INSERT INTO package_files (version_id, repository_id, path, filename, blob_id) SELECT id, $1, $2, $2, $3 FROM v`,
					s.repo, fmt.Sprintf("f%d", i), id)

				return err
			})
			if err != nil {
				errs <- fmt.Errorf("upload %d: %w", i, err)
			}
		})
	}

	for range 3 {
		wg.Go(func() {
			if _, err := pg.DeleteBlobsIfUnreferenced(ctx, shas); err != nil {
				errs <- fmt.Errorf("batch delete: %w", err)
			}
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	for i, sha := range shas {
		if referenced[i] {
			if !blobExists(t, s, sha) {
				t.Errorf("blob %d is referenced by a package file but its row is gone", i)
			}

			if got := s.queryID(
				`SELECT count(*)::text FROM package_files f JOIN blobs b ON b.id = f.blob_id WHERE b.sha256 = $1`,
				sha,
			); got != "1" {
				t.Errorf("blob %d: %s package files reference it, want 1", i, got)
			}
		}
	}
}

// QuarantinedKeys returns the keys of quarantined artifacts only.
func TestQuarantinedKeys(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "quarantined-keys", "helm")

	for _, k := range []string{"a", "b"} {
		ref := s.queryID(`INSERT INTO artifact_refs (repository_id, artifact_path, format) VALUES ($1, $2, 'helm') RETURNING id`, s.repo, k)
		job := s.queryID(`INSERT INTO scan_jobs (artifact_ref_id, scan_type, status) VALUES ($1, 'secret', 'completed') RETURNING id`, ref)

		s.exec(`INSERT INTO secret_results (scan_job_id, quarantined) VALUES ($1, $2)`, job, k == "a")
	}

	got, err := pg.QuarantinedKeys(ctx, s.repo, []string{"a", "b", "c"})
	if err != nil || len(got) != 1 || got[0] != "a" {
		t.Fatalf("QuarantinedKeys = %v, %v; want [a]", got, err)
	}

	if got, err := pg.QuarantinedKeys(ctx, s.repo, nil); err != nil || got != nil {
		t.Fatalf("no keys = %v, %v", got, err)
	}
}

// A capped count is exact up to the ceiling and the lower bound ceiling+1
// beyond it, for the plain listing, a broad q and a format filter alike; a
// page past the cap still knows the listing goes on.
func TestComponentTotalCap(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "total-cap", "helm")

	for range 30 {
		s.helm(s.blob(1))
	}

	for _, tc := range []struct {
		name      string
		opts      ComponentListOptions
		total     int
		wantExact bool
	}{
		{"uncapped plain", ComponentListOptions{Page: 1, Limit: 5}, 30, true},
		{"capped below", ComponentListOptions{Page: 1, Limit: 5, TotalCap: 100}, 30, true},
		{"capped plain", ComponentListOptions{Page: 1, Limit: 5, TotalCap: 10}, 11, false},
		{"capped broad q", ComponentListOptions{Query: "chart", Page: 1, Limit: 5, TotalCap: 10}, 11, false},
		{"exact q", ComponentListOptions{Query: "chart", Page: 1, Limit: 5}, 30, true},
		{"capped format", ComponentListOptions{Format: "helm", Page: 1, Limit: 5, TotalCap: 10}, 11, false},
		{"deep page lifts the ceiling", ComponentListOptions{Page: 4, Limit: 5, TotalCap: 10}, 21, false},
		{"deep page inside the data", ComponentListOptions{Page: 6, Limit: 5, TotalCap: 10}, 30, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, total, err := pg.SearchComponents(ctx, s.repo, tc.opts)
			if err != nil {
				t.Fatal(err)
			}

			if len(rows) == 0 {
				t.Fatal("no rows")
			}

			if total != tc.total || tc.opts.TotalExact(total) != tc.wantExact {
				t.Fatalf("total = %d exact = %v, want %d %v", total, tc.opts.TotalExact(total), tc.total, tc.wantExact)
			}
		})
	}
}
