// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestDockerBlobImageVisibility(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "docker-links", "docker")

	sha := strings.Repeat("a1", 32)
	blob := &models.DockerBlob{RepositoryID: s.repo, Digest: "sha256:" + sha, BlobSHA256: sha, Size: 7, MediaType: "application/octet-stream"}
	imageA, imageB := "team-a/app", "team-b/app"
	blob.Image = &imageA

	if err := pg.CreateDockerBlob(ctx, blob); err != nil {
		t.Fatal(err)
	}

	visible := func(image string) bool {
		t.Helper()

		v, err := pg.DockerBlobVisible(ctx, s.repo, image, blob.Digest)
		if err != nil {
			t.Fatal(err)
		}

		return v
	}

	if !visible(imageA) || visible(imageB) {
		t.Fatalf("after push to A: A=%v B=%v, want true/false", visible(imageA), visible(imageB))
	}

	if err := pg.LinkDockerBlob(ctx, s.repo, imageB, blob.Digest); err != nil {
		t.Fatal(err)
	}

	if !visible(imageB) {
		t.Fatal("blob not visible to B after link")
	}

	// A blob that predates image links (no rows) is visible to every image.
	legacySHA := strings.Repeat("b2", 32)
	legacy := &models.DockerBlob{RepositoryID: s.repo, Digest: "sha256:" + legacySHA, BlobSHA256: legacySHA, Size: 3}

	if err := pg.CreateDockerBlob(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	if v, err := pg.DockerBlobVisible(ctx, s.repo, imageB, legacy.Digest); err != nil || !v {
		t.Fatalf("unlinked blob visible = %v, %v, want true", v, err)
	}

	// The size verified at upload is what the registry serves.
	got, err := pg.GetDockerBlob(ctx, s.repo, blob.Digest)
	if err != nil || got == nil || got.Size != 7 {
		t.Fatalf("GetDockerBlob = %+v, %v, want size 7", got, err)
	}
}

func TestCreateDockerManifestChecksBlobsUnderLock(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "docker-reqblobs", "docker")

	sha := strings.Repeat("c3", 32)
	image := "app"

	if err := pg.CreateDockerBlob(ctx, &models.DockerBlob{RepositoryID: s.repo, Digest: "sha256:" + sha, BlobSHA256: sha, Size: 1, Image: &image}); err != nil {
		t.Fatal(err)
	}

	mk := func(img string, required ...string) error {
		return pg.CreateDockerManifest(ctx, &models.DockerManifest{
			RepositoryID:  s.repo,
			Image:         img,
			Digest:        "sha256:" + strings.Repeat("d4", 32),
			MediaType:     "application/vnd.oci.image.manifest.v1+json",
			BlobID:        s.blob(10),
			Size:          10,
			Refs:          &models.DockerManifestRefs{Blobs: required},
			RequiredBlobs: required,
		})
	}

	if err := mk(image, "sha256:"+sha); err != nil {
		t.Fatalf("manifest with an available blob: %v", err)
	}

	if err := mk(image, "sha256:"+strings.Repeat("e5", 32)); !errors.Is(err, ErrDockerBlobsMissing) {
		t.Fatalf("manifest with a missing blob = %v, want ErrDockerBlobsMissing", err)
	}

	if err := mk("other", "sha256:"+sha); !errors.Is(err, ErrDockerBlobsMissing) {
		t.Fatalf("manifest of an image the blob is not linked to = %v, want ErrDockerBlobsMissing", err)
	}
}

func TestUpsertBlobKeepsFirstMediaTypeAndFixesZeroSize(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()

	sha := strings.Repeat("f6", 32)

	if _, err := pg.upsertBlobWithMediaType(ctx, sha, sha, "application/gzip", 0); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.upsertBlobWithMediaType(ctx, sha, sha, "text/html", 42); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.upsertBlobWithMediaType(ctx, sha, sha, "text/plain", 7); err != nil {
		t.Fatal(err)
	}

	var (
		mt   string
		size int64
	)

	if err := pg.GetDB().QueryRowContext(ctx, `SELECT media_type, size_bytes FROM blobs WHERE sha256 = $1`, sha).Scan(&mt, &size); err != nil {
		t.Fatal(err)
	}

	if mt != "application/gzip" || size != 42 {
		t.Fatalf("blobs row = (%q, %d), want first media type and the first non-zero size (application/gzip, 42)", mt, size)
	}
}

func TestUpdateDockerUploadOffsetIsCompareAndSet(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "docker-cas", "docker")

	up := &models.DockerUpload{RepositoryID: s.repo, UploadUUID: "11111111-1111-4111-8111-111111111111"}
	if err := pg.CreateDockerUpload(ctx, up); err != nil {
		t.Fatal(err)
	}

	if ok, err := pg.UpdateDockerUploadOffset(ctx, s.repo, up.UploadUUID, 0, 10); err != nil || !ok {
		t.Fatalf("first advance = %v, %v, want true", ok, err)
	}

	// A second request that also started from offset 0 loses.
	if ok, err := pg.UpdateDockerUploadOffset(ctx, s.repo, up.UploadUUID, 0, 99); err != nil || ok {
		t.Fatalf("stale advance = %v, %v, want false", ok, err)
	}

	got, err := pg.GetDockerUpload(ctx, s.repo, up.UploadUUID)
	if err != nil || got.Offset != 10 {
		t.Fatalf("offset = %+v, %v, want 10", got, err)
	}
}
