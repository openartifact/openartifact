// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// mavenTestArtifact is the snapshot file every writer overwrites.
type mavenTestArtifact struct{ models.MavenArtifact }

func (a *mavenTestArtifact) set(repoID, blobID string) {
	a.RepositoryID, a.BlobID = repoID, blobID
	a.GroupID, a.ArtifactCoord, a.Version = "g", "a", "1.0-SNAPSHOT"
	a.Extension, a.IsSnapshot = "jar", true
	a.RepoPath = "g/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.jar"
}

// Concurrent overwrites of one maven snapshot path serialize on the path's
// lock, so each reports the row it actually replaced: after every writer
// reserved its size and released that of the row it replaced, exactly the
// winner's usage is left.
func TestConcurrentMavenOverwritesKeepOneFileAndConsistentUsage(t *testing.T) {
	pg := newQuotaTestDB(t, 20)
	s := newQuotaSeeder(t, pg, "maven-overwrite", "maven")
	ctx := context.Background()

	const writers = 8

	sizes := make([]int64, writers)
	blobIDs := make([]string, writers)

	for i := range writers {
		sizes[i] = int64(10 * (i + 1))
		blobIDs[i] = s.blob(sizes[i])
	}

	var (
		wg   sync.WaitGroup
		errs = make([]error, writers)
	)

	for i := range writers {
		wg.Go(func() {
			reserveN(t, pg, s.repo, sizes[i], 1)

			a := &mavenTestArtifact{}
			a.set(s.repo, blobIDs[i])

			if errs[i] = pg.CreateMavenArtifact(ctx, &a.MavenArtifact); errs[i] != nil {
				return
			}

			if r := a.Replaced; r != nil {
				errs[i] = pg.DecrementStorageUsage(ctx, s.repo, r.SizeBytes, 1)
			}
		})
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	var files int

	var winner int64

	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT count(*), COALESCE(max(b.size_bytes), 0) FROM package_files f JOIN blobs b ON b.id = f.blob_id
		 WHERE f.repository_id = $1 AND f.deleted_at IS NULL`, s.repo).Scan(&files, &winner); err != nil {
		t.Fatalf("read files: %v", err)
	}

	if files != 1 {
		t.Fatalf("%d live files, want 1", files)
	}

	requireUsage(t, pg, s.repo, winner, 1)

	if n := pendingEntries(t, pg, s.repo); n != 0 {
		t.Errorf("%d pending entries left, want 0", n)
	}
}
