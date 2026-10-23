// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// emptyPackagesPath is the index every repository with an empty main/amd64
// component renders; its content (zero bytes) is therefore one blob shared by
// all such repositories.
const emptyPackagesPath = "dists/bookworm/main/binary-amd64/Packages"

func emptySHA() string {
	sum := sha256.Sum256(nil)

	return hex.EncodeToString(sum[:])
}

// debRepo creates a deb repository with distribution bookworm, component main
// and architecture amd64 and returns its id and the distribution id.
func debRepo(t *testing.T, pg *database.PostgresDB, name string) (string, string) {
	t.Helper()

	ctx := context.Background()
	org := trashTestOrgID

	repo := &models.Repository{Name: name, Format: "deb", RepoType: "hosted", ProjectID: &org}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	dist, err := pg.GetOrCreateDebianDistribution(ctx, repo.ID, "bookworm")
	if err != nil {
		t.Fatalf("GetOrCreateDebianDistribution: %v", err)
	}

	if _, err := pg.GetOrCreateDebianComponent(ctx, dist.ID, "main"); err != nil {
		t.Fatalf("GetOrCreateDebianComponent: %v", err)
	}

	if _, err := pg.GetOrCreateDebianArchitecture(ctx, dist.ID, "amd64"); err != nil {
		t.Fatalf("GetOrCreateDebianArchitecture: %v", err)
	}

	return repo.ID, dist.ID
}

func metadataSHAs(t *testing.T, pg *database.PostgresDB, repoID string) map[string]string {
	t.Helper()

	rows, err := pg.GetDB().QueryContext(context.Background(), `SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1`, repoID)
	if err != nil {
		t.Fatalf("list repository_metadata: %v", err)
	}

	defer func() { _ = rows.Close() }()

	out := make(map[string]string)

	for rows.Next() {
		var path, sum string
		if err := rows.Scan(&path, &sum); err != nil {
			t.Fatal(err)
		}

		out[path] = sum
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

// requireBlobsPresent fails for every metadata row of repoID whose object is
// missing from the blob store: such a row would 404 for every apt client.
func requireBlobsPresent(t *testing.T, store *formattest.BlobStore, pg *database.PostgresDB, repoID string) {
	t.Helper()

	rows := metadataSHAs(t, pg, repoID)
	if len(rows) == 0 {
		t.Fatalf("repository %s has no metadata rows", repoID)
	}

	for path, sum := range rows {
		ok, err := store.Exists(context.Background(), sum)
		if err != nil {
			t.Fatalf("exists(%s): %v", sum, err)
		}

		if !ok {
			t.Errorf("metadata row %s of repository %s points at blob %s, which is not in the blob store", path, repoID, sum)
		}
	}
}

// putHookStore runs onPut after the first Put that matches match.
type putHookStore struct {
	*formattest.BlobStore

	match func(metadata map[string]string) bool
	onPut func()
	fired bool
}

func (s *putHookStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	id, err := s.BlobStore.Put(ctx, data, metadata)
	if err != nil {
		return id, err
	}

	if !s.fired && s.match(metadata) {
		s.fired = true
		s.onPut()
	}

	return id, nil
}

// TestDebRegenerateKeepsBlobSharedByConcurrentRepo is the DEB-W race: repository
// A stops generating an index whose (empty, hence identical) content repository
// B is just storing. A's reclaim runs while B's rows are still uncommitted, so
// the reference counts see no user of the blob and delete it; B then commits a
// row whose object is gone. The committed set must be repaired.
func TestDebRegenerateKeepsBlobSharedByConcurrentRepo(t *testing.T) {
	pg := newTrashPurgeTestDB(t)
	store := formattest.NewBlobStore()
	ctx := context.Background()
	empty := emptySHA()

	repoA, distA := debRepo(t, pg, "deb-race-a")
	repoB, _ := debRepo(t, pg, "deb-race-b")

	wA := NewDebMetadataWorker(pg, store, nil, testLogger(t))
	t.Cleanup(wA.Stop)

	if err := wA.regenerate(ctx, repoA); err != nil {
		t.Fatalf("initial regenerate A: %v", err)
	}

	if got := metadataSHAs(t, pg, repoA)[emptyPackagesPath]; got != empty {
		t.Fatalf("A's %s = %q, want the empty-content blob %q", emptyPackagesPath, got, empty)
	}

	// A drops its component: the next run no longer generates main/*, so the
	// empty Packages blob is superseded for A.
	if _, err := pg.GetDB().ExecContext(ctx, `DELETE FROM debian_components WHERE distribution_id = $1`, distA); err != nil {
		t.Fatalf("delete component: %v", err)
	}

	hooked := &putHookStore{
		BlobStore: store,
		match: func(md map[string]string) bool {
			return md["repo_id"] == repoB && md["sha256"] == empty
		},
		onPut: func() {
			// A regenerates and reclaims while B holds the blob in a
			// still-open transaction.
			if err := wA.regenerate(ctx, repoA); err != nil {
				t.Errorf("concurrent regenerate A: %v", err)
			}
		},
	}

	wB := NewDebMetadataWorker(pg, hooked, nil, testLogger(t))
	t.Cleanup(wB.Stop)

	if err := wB.regenerate(ctx, repoB); err != nil {
		t.Fatalf("regenerate B: %v", err)
	}

	if !hooked.fired {
		t.Fatal("test bug: the hook never fired, the race was not exercised")
	}

	requireBlobsPresent(t, store, pg, repoB)
	requireBlobsPresent(t, store, pg, repoA)
}

// TestDebRegenerateReclaimsOnlyUnreferencedMetadataBlobs checks both sides of
// the reclaim: a superseded blob nothing references is deleted, one that
// another repository's committed row still points at is kept.
func TestDebRegenerateReclaimsOnlyUnreferencedMetadataBlobs(t *testing.T) {
	pg := newTrashPurgeTestDB(t)
	store := formattest.NewBlobStore()
	ctx := context.Background()
	empty := emptySHA()

	repoA, distA := debRepo(t, pg, "deb-reclaim-a")
	repoB, _ := debRepo(t, pg, "deb-reclaim-b")

	wA := NewDebMetadataWorker(pg, store, nil, testLogger(t))
	t.Cleanup(wA.Stop)

	for _, id := range []string{repoA, repoB} {
		if err := wA.regenerate(ctx, id); err != nil {
			t.Fatalf("regenerate %s: %v", id, err)
		}
	}

	if _, err := pg.GetDB().ExecContext(ctx, `DELETE FROM debian_components WHERE distribution_id = $1`, distA); err != nil {
		t.Fatalf("delete component: %v", err)
	}

	if err := wA.regenerate(ctx, repoA); err != nil {
		t.Fatalf("regenerate A after dropping the component: %v", err)
	}

	if _, ok := metadataSHAs(t, pg, repoA)[emptyPackagesPath]; ok {
		t.Fatalf("A still has a row for %s", emptyPackagesPath)
	}

	ok, err := store.Exists(ctx, empty)
	if err != nil || !ok {
		t.Fatalf("blob %s shared with B was deleted (exists=%v err=%v)", empty, ok, err)
	}

	requireBlobsPresent(t, store, pg, repoB)

	// B drops its component too; now nothing references the blob.
	var distB string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT id FROM debian_distributions WHERE repository_id = $1`, repoB).Scan(&distB); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.GetDB().ExecContext(ctx, `DELETE FROM debian_components WHERE distribution_id = $1`, distB); err != nil {
		t.Fatalf("delete component B: %v", err)
	}

	if err := wA.regenerate(ctx, repoB); err != nil {
		t.Fatalf("regenerate B after dropping the component: %v", err)
	}

	// The reclaim queues the bytes; the sweeper removes them.
	if _, err := NewBlobDeletionSweeper(pg, copyDeleter{store}, testLogger(t), 0, 500).RunOnce(ctx); err != nil {
		t.Fatalf("sweep queued blob deletions: %v", err)
	}

	if ok, err := store.Exists(ctx, empty); err != nil || ok {
		t.Fatalf("blob %s is unreferenced but still stored (exists=%v err=%v)", empty, ok, err)
	}
}
