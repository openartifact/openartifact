// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
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

	s.exec(`INSERT INTO repository_metadata (repository_id, path, blob_sha256, blob_size) VALUES ($1, 'maven-metadata.xml', $2, 1)`, s.repo, metaSHA)
	s.exec(`INSERT INTO package_deltas (repository_id, from_sha256, from_filename, to_sha256, to_filename, delta_sha256, delta_size, format)
	        VALUES ($1, $2, 'a', $3, 'b', $4, 1, 'rpm')`, s.repo, strings.Repeat("d", 64), strings.Repeat("e", 64), deltaSHA)

	// A raw upload of the same bytes leaves a blobs row with no package row (after its delete).
	for _, sha := range []string{metaSHA, deltaSHA, bareSHA} {
		s.exec(`INSERT INTO blobs (sha256, size_bytes, blob_key, created_at) VALUES ($1::varchar, 1, $1::text, now() - interval '2 hours')`, sha)
	}

	for name, sha := range map[string]string{"metadata": metaSHA, "delta": deltaSHA} {
		n, err := pg.BlobReferenceCount(ctx, sha)
		if err != nil || n != 1 {
			t.Fatalf("%s: BlobReferenceCount = %d, %v; want 1", name, n, err)
		}

		deleted := false

		removed, err := pg.DeleteBlobIfUnreferenced(ctx, sha, func(context.Context) error {
			deleted = true

			return nil
		})
		if err != nil || removed || deleted {
			t.Fatalf("%s: DeleteBlobIfUnreferenced = %v, %v (storage deleted: %v); want kept", name, removed, err, deleted)
		}
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
	deleted := false

	removed, err := pg.DeleteBlobIfUnreferenced(ctx, ghost, func(context.Context) error {
		deleted = true

		return nil
	})
	if err != nil || !removed || !deleted {
		t.Fatalf("unreferenced sha without a blobs row: removed=%v deleted=%v err=%v; want storage delete", removed, deleted, err)
	}
}
