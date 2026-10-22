// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/worker"
)

// runMetadataRegen rebuilds repoID's RPM metadata synchronously.
func runMetadataRegen(t *testing.T, repoID string) {
	t.Helper()
	w := worker.NewRPMetadataWorker(harness.Database, harness.BlobStore, nil, harness.Log, nil)
	if err := w.Regenerate(context.Background(), repoID); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
}

func TestRPMMetadataRegeneration(t *testing.T) {
	harness.Reset(t)
	ctx := context.Background()

	repo := createTestRepo(t, ctx, "meta-regen-repo")
	rpmData := CreateTestRPM("metapkg", "1.0.0", "1.el9", "x86_64")
	router := buildUploadRouter(t)
	w := uploadRPM(t, router, rpmData, repo.Name)
	if w.Code != 200 {
		t.Fatalf("upload failed: %d %s", w.Code, w.Body.String())
	}

	if err := harness.Database.MarkRepoStale(ctx, repo.ID); err != nil {
		t.Fatalf("MarkRepoStale: %v", err)
	}

	runMetadataRegen(t, repo.ID)

	metas, err := harness.Database.GetRepoMetadataByRepo(ctx, repo.ID)
	if err != nil {
		t.Fatalf("GetRepoMetadataByRepo: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("expected repo_metadata entries after regeneration")
	}

	paths := make(map[string]bool)
	for _, m := range metas {
		paths[m.Path] = true
	}
	for _, want := range []string{
		"repodata/repomd.xml",
	} {
		if !paths[want] {
			t.Errorf("missing expected path %q in repo_metadata", want)
		}
	}

	hasPrimary := false
	hasFilelists := false
	hasOther := false
	for p := range paths {
		if strings.Contains(p, "primary.xml.gz") {
			hasPrimary = true
		}
		if strings.Contains(p, "filelists.xml.gz") {
			hasFilelists = true
		}
		if strings.Contains(p, "other.xml.gz") {
			hasOther = true
		}
	}
	if !hasPrimary {
		t.Error("missing primary.xml.gz in repo_metadata")
	}
	if !hasFilelists {
		t.Error("missing filelists.xml.gz in repo_metadata")
	}
	if !hasOther {
		t.Error("missing other.xml.gz in repo_metadata")
	}
}

func TestRPMMetadataRegeneration_StaleCleared(t *testing.T) {
	harness.Reset(t)
	ctx := context.Background()

	repo := createTestRepo(t, ctx, "meta-stale-repo")
	rpmData := CreateTestRPM("stalepkg", "1.0.0", "1.el9", "x86_64")
	router := buildUploadRouter(t)
	w := uploadRPM(t, router, rpmData, repo.Name)
	if w.Code != 200 {
		t.Fatalf("upload failed: %d %s", w.Code, w.Body.String())
	}

	if err := harness.Database.MarkRepoStale(ctx, repo.ID); err != nil {
		t.Fatalf("MarkRepoStale: %v", err)
	}

	runMetadataRegen(t, repo.ID)

	rpmRepo, err := harness.Database.GetRPMRepo(ctx, repo.ID)
	if err != nil {
		t.Fatalf("GetRPMRepo: %v", err)
	}
	if rpmRepo == nil {
		t.Fatal("expected rpm_repos row")
	}
	if rpmRepo.MetadataStale {
		t.Error("expected metadata_stale = false after regeneration")
	}
}

func TestRPMMetadataRegeneration_BlobsExist(t *testing.T) {
	harness.Reset(t)
	ctx := context.Background()

	repo := createTestRepo(t, ctx, "meta-blobs-repo")
	rpmData := CreateTestRPM("blobmetapkg", "1.0.0", "1.el9", "x86_64")
	router := buildUploadRouter(t)
	w := uploadRPM(t, router, rpmData, repo.Name)
	if w.Code != 200 {
		t.Fatalf("upload failed: %d %s", w.Code, w.Body.String())
	}

	if err := harness.Database.MarkRepoStale(ctx, repo.ID); err != nil {
		t.Fatalf("MarkRepoStale: %v", err)
	}

	runMetadataRegen(t, repo.ID)

	metas, err := harness.Database.GetRepoMetadataByRepo(ctx, repo.ID)
	if err != nil {
		t.Fatalf("GetRepoMetadataByRepo: %v", err)
	}

	for _, m := range metas {
		if len(m.BlobSHA256) != 64 {
			t.Errorf("path %q: blob_sha256 length = %d, want 64", m.Path, len(m.BlobSHA256))
		}
		exists, err := harness.BlobStore.Exists(ctx, m.BlobSHA256)
		if err != nil {
			t.Errorf("path %q: BlobStore.Exists error: %v", m.Path, err)
			continue
		}
		if !exists {
			t.Errorf("path %q: blob %q not found in store", m.Path, m.BlobSHA256)
		}
	}
}

// regeneratedPaths returns path -> blob sha256 of the generated metadata rows.
func regeneratedPaths(t *testing.T, ctx context.Context, repoID string) map[string]string {
	t.Helper()

	metas, err := harness.Database.GetRepoMetadataByRepo(ctx, repoID)
	if err != nil {
		t.Fatalf("GetRepoMetadataByRepo: %v", err)
	}

	out := make(map[string]string, len(metas))
	for _, m := range metas {
		out[m.Path] = m.BlobSHA256
	}

	return out
}

func markStaleAndRegen(t *testing.T, ctx context.Context, repoID string) {
	t.Helper()

	if err := harness.Database.MarkRepoStale(ctx, repoID); err != nil {
		t.Fatalf("MarkRepoStale: %v", err)
	}

	runMetadataRegen(t, repoID)
}

// Regenerating metadata must drop the replaced primary/filelists/other rows and
// their blobs, keep comps.xml, and never delete a blob another repository's
// metadata still references.
func TestRPMMetadataRegeneration_SupersededCleanedUp(t *testing.T) {
	harness.Reset(t)
	ctx := context.Background()
	router := buildUploadRouter(t)

	repoX := createTestRepo(t, ctx, "meta-clean-x")
	repoY := createTestRepo(t, ctx, "meta-clean-y")

	pkgA := CreateTestRPM("cleanpkg", "1.0.0", "1.el9", "x86_64")
	for _, name := range []string{repoX.Name, repoY.Name} {
		if w := uploadRPM(t, router, pkgA, name); w.Code != 200 {
			t.Fatalf("upload to %s: %d %s", name, w.Code, w.Body.String())
		}
	}

	// primary.xml carries each package's upload time (<time file=>); the two
	// uploads can straddle a second boundary, so pin them to one instant.
	if _, err := harness.DB.ExecContext(ctx,
		`UPDATE rpm_packages SET created_at = date_trunc('second', now()) WHERE repository_id IN ($1, $2)`,
		repoX.ID, repoY.ID); err != nil {
		t.Fatalf("pin upload time: %v", err)
	}

	if err := harness.Database.UpsertRepoMetadata(ctx, &models.RepoMetadata{
		RepositoryID: repoX.ID, Path: "comps.xml", BlobSHA256: strings.Repeat("c", 64), BlobSize: 8,
	}); err != nil {
		t.Fatalf("UpsertRepoMetadata comps: %v", err)
	}

	markStaleAndRegen(t, ctx, repoX.ID)
	markStaleAndRegen(t, ctx, repoY.ID)

	oldX := regeneratedPaths(t, ctx, repoX.ID)
	oldY := regeneratedPaths(t, ctx, repoY.ID)

	requireSharedPrimary(t, oldX, oldY)

	// Change X's package set and regenerate.
	if w := uploadRPM(t, router, CreateTestRPM("cleanpkg2", "2.0.0", "1.el9", "x86_64"), repoX.Name); w.Code != 200 {
		t.Fatalf("second upload: %d %s", w.Code, w.Body.String())
	}

	markStaleAndRegen(t, ctx, repoX.ID)

	newX := regeneratedPaths(t, ctx, repoX.ID)
	if _, ok := newX["comps.xml"]; !ok {
		t.Error("comps.xml row must be preserved")
	}

	assertSupersededHandled(t, ctx, oldX, oldY, newX)
	assertRowsHaveBlobs(t, ctx, newX)
	assertRowsHaveBlobs(t, ctx, regeneratedPaths(t, ctx, repoY.ID))
}

// requireSharedPrimary fails unless both repos reference the same primary.xml.gz blob
// (same package set => identical generated content).
func requireSharedPrimary(t *testing.T, x, y map[string]string) {
	t.Helper()

	for path, sum := range x {
		if strings.HasSuffix(path, "-primary.xml.gz") && y[path] == sum {
			return
		}
	}

	t.Fatal("expected repos X and Y to share the primary.xml.gz blob")
}

func containsSHA(rows map[string]string, sum string) bool {
	for _, s := range rows {
		if s == sum {
			return true
		}
	}

	return false
}

// assertSupersededHandled checks every old generated row of repo X: replaced rows
// are gone, blobs only X used are deleted, blobs repo Y still references survive.
func assertSupersededHandled(t *testing.T, ctx context.Context, oldX, oldY, newX map[string]string) {
	t.Helper()

	for path, sum := range oldX {
		if path == "comps.xml" || path == "repodata/repomd.xml" {
			continue
		}

		if _, still := newX[path]; still {
			t.Errorf("superseded row %q still present", path)
		}

		if containsSHA(newX, sum) {
			continue // still part of X's new set
		}

		exists, err := harness.BlobStore.Exists(ctx, sum)
		if err != nil {
			t.Fatalf("Exists(%s): %v", path, err)
		}

		switch onlyX := !containsSHA(oldY, sum); {
		case onlyX && exists:
			t.Errorf("superseded blob of %q (%s) must be deleted", path, sum)
		case !onlyX && !exists:
			t.Errorf("blob of %q is still referenced by repo Y but was deleted", path)
		}
	}
}

// assertRowsHaveBlobs checks every generated row resolves to a stored blob.
func assertRowsHaveBlobs(t *testing.T, ctx context.Context, rows map[string]string) {
	t.Helper()

	for path, sum := range rows {
		if path == "comps.xml" {
			continue
		}

		if ok, err := harness.BlobStore.Exists(ctx, sum); err != nil || !ok {
			t.Errorf("row %q references missing blob %s (err=%v)", path, sum, err)
		}
	}
}
