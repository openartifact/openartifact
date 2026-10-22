// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// PruneRepoDeltas keeps the newest deltas within the count and byte caps and
// returns the blobs of the pruned rows; a delta patch blob counts as referenced
// until its row is gone.
func TestPruneRepoDeltasAndBlobRefs(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "prune-deltas", "rpm")

	patchSHA := func(blobID string) string { return s.queryID(`SELECT sha256 FROM blobs WHERE id = $1`, blobID) }

	var patches []string

	for i := range 4 {
		sha := patchSHA(s.blob(100))
		patches = append(patches, sha)

		if err := pg.CreatePackageDelta(ctx, &models.PackageDelta{
			RepositoryID: s.repo, FromSHA256: fmt.Sprintf("%064d", i), FromFilename: "f", ToSHA256: fmt.Sprintf("%064d", 99),
			ToFilename: "t", DeltaSHA256: sha, DeltaSize: 100, Format: "rpm",
		}); err != nil {
			t.Fatal(err)
		}

		// created_at orders the pruning: older rows go first.
		s.exec(`UPDATE package_deltas SET created_at = now() - make_interval(mins => $2) WHERE delta_sha256 = $1`, sha, 10-i)
	}

	// A delta blob is a reference: never reported unreferenced while its row exists.
	n, err := pg.BlobReferenceCount(ctx, patches[0])
	if err != nil || n != 1 {
		t.Fatalf("BlobReferenceCount(delta blob) = %d, %v; want 1", n, err)
	}

	// Count cap 3 removes the single oldest; byte cap 250 then keeps two.
	pruned, err := pg.PruneRepoDeltas(ctx, s.repo, 3, 1<<30)
	if err != nil || len(pruned) != 1 || pruned[0] != patches[0] {
		t.Fatalf("count prune = %v, %v; want [%s]", pruned, err, patches[0])
	}

	pruned, err = pg.PruneRepoDeltas(ctx, s.repo, 10, 250)
	if err != nil || len(pruned) != 1 || pruned[0] != patches[1] {
		t.Fatalf("byte prune = %v, %v; want [%s]", pruned, err, patches[1])
	}

	// Once its row is gone the patch blob is unreferenced and reclaimable.
	if n, _ := pg.BlobReferenceCount(ctx, patches[0]); n != 0 {
		t.Errorf("pruned delta blob still counted: %d", n)
	}

	unref, _, err := pg.ListUnreferencedBlobs(ctx, time.Duration(0), "", 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	for _, u := range unref {
		found[u] = true
	}

	if !found[patches[0]] || found[patches[3]] {
		t.Errorf("unreferenced = %v; want pruned %s present, live %s absent", unref, patches[0], patches[3])
	}
}
