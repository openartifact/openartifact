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

// The generic package tables hold every format's rows; the pypi and helm
// reads must see only their own format's.
func TestFormatScopedReads(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "scoped-reads", "pypi")

	chart := &models.HelmChart{
		RepositoryID: s.repo, Name: "demo", Version: "1.0.0", APIVersion: "v2",
		BlobKey: testSHA("chart"), Digest: testSHA("chart"), SizeBytes: 5,
	}
	if err := pg.CreateHelmChart(ctx, chart); err != nil {
		t.Fatalf("CreateHelmChart: %v", err)
	}

	dist := &models.PyPIPackage{
		RepositoryID: s.repo, Name: "other", Version: "1.0", Filename: "other-1.0.tar.gz", PackageType: "sdist",
		SHA256Digest: testSHA("dist"), BlobKey: testSHA("dist"), SizeBytes: 4,
	}
	if err := pg.CreatePyPIPackage(ctx, dist); err != nil {
		t.Fatalf("CreatePyPIPackage: %v", err)
	}

	charts, err := pg.ListHelmCharts(ctx, s.repo)
	if err != nil {
		t.Fatal(err)
	}

	if len(charts) != 1 || charts[0].Name != "demo" {
		t.Fatalf("ListHelmCharts = %d charts, want only the helm one", len(charts))
	}

	for filename, want := range map[string]bool{"other-1.0.tar.gz": true, "demo-1.0.0.tgz": false} {
		got, err := pg.GetPyPIPackageByFilename(ctx, s.repo, filename)
		if err != nil {
			t.Fatalf("GetPyPIPackageByFilename(%s): %v", filename, err)
		}

		if (got != nil) != want {
			t.Errorf("GetPyPIPackageByFilename(%s) found = %v, want %v", filename, got != nil, want)
		}
	}
}

// A raw write at the path of another format's file must leave that file alone.
func TestUpsertRawArtifactKeepsForeignFiles(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "raw-foreign", "pypi")

	dist := &models.PyPIPackage{
		RepositoryID: s.repo, Name: "demo", Version: "1.0", Filename: "demo-1.0.tar.gz", PackageType: "sdist",
		SHA256Digest: testSHA("orig"), BlobKey: testSHA("orig"), SizeBytes: 4,
	}
	if err := pg.CreatePyPIPackage(ctx, dist); err != nil {
		t.Fatalf("CreatePyPIPackage: %v", err)
	}

	path := s.queryID(`SELECT path FROM package_files WHERE repository_id = $1`, s.repo)
	evil := testSHA("evil")

	err := pg.UpsertRawArtifact(ctx, &models.RawArtifact{
		RepositoryID: s.repo, Path: path, ContentType: "text/plain", SHA256: evil, BlobKey: evil, SizeBytes: 4,
	})
	if !errors.Is(err, ErrRawPathForeign) {
		t.Fatalf("UpsertRawArtifact over a pypi file = %v, want ErrRawPathForeign", err)
	}

	got, err := pg.GetPyPIPackageByFilename(ctx, s.repo, "demo-1.0.tar.gz")
	if err != nil || got == nil {
		t.Fatalf("GetPyPIPackageByFilename = %v, %v", got, err)
	}

	if got.SHA256Digest != dist.SHA256Digest {
		t.Fatalf("pypi file digest = %s, want the original %s", got.SHA256Digest, dist.SHA256Digest)
	}
}

func TestDeleteDockerBlobFromImage(t *testing.T) {
	const (
		imageA = "team-a/app"
		imageB = "team-b/app"
	)

	tests := []struct {
		name string
		// setup seeds the repository; layer is the digest under test.
		setup func(t *testing.T, pg *PostgresDB, repo, layer string)
		image string
		want  DockerBlobDeletion
		// rowKept: the repository still holds the blob record afterwards.
		rowKept bool
		// visibleToA/visibleToB: the blob is still served to the image.
		visibleToA, visibleToB bool
	}{
		{
			name: "sole link and no manifest removes the record",
			setup: func(t *testing.T, pg *PostgresDB, repo, layer string) {
				t.Helper()
				pushDockerBlob(t, pg, repo, layer, imageA)
			},
			image: imageA, want: DockerBlobRemoved,
		},
		{
			name: "blob shared with another image only loses the link",
			setup: func(t *testing.T, pg *PostgresDB, repo, layer string) {
				t.Helper()
				pushDockerBlob(t, pg, repo, layer, imageA)
				linkDockerBlob(t, pg, repo, layer, imageB)
			},
			image: imageA, want: DockerBlobUnlinked, rowKept: true, visibleToB: true,
		},
		{
			name: "live manifest of the image refuses",
			setup: func(t *testing.T, pg *PostgresDB, repo, layer string) {
				t.Helper()
				pushDockerBlob(t, pg, repo, layer, imageA)
				pushDockerManifest(t, pg, repo, imageA, "sha256:"+strings.Repeat("11", 32), layer)
			},
			image: imageA, want: DockerBlobInUse, rowKept: true, visibleToA: true,
		},
		{
			name: "manifest of another image keeps the record",
			setup: func(t *testing.T, pg *PostgresDB, repo, layer string) {
				t.Helper()
				pushDockerBlob(t, pg, repo, layer, imageA)
				linkDockerBlob(t, pg, repo, layer, imageB)
				pushDockerManifest(t, pg, repo, imageB, "sha256:"+strings.Repeat("22", 32), layer)
			},
			image: imageA, want: DockerBlobUnlinked, rowKept: true, visibleToB: true,
		},
		{
			name: "unlinked blob a manifest of another image uses refuses",
			setup: func(t *testing.T, pg *PostgresDB, repo, layer string) {
				t.Helper()
				pushDockerBlob(t, pg, repo, layer, imageB)
				pushDockerManifest(t, pg, repo, imageB, "sha256:"+strings.Repeat("33", 32), layer)
				deleteDockerBlobLinks(t, pg, repo, layer)
			},
			image: imageA, want: DockerBlobInUse, rowKept: true, visibleToA: true, visibleToB: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg := newQuotaTestDB(t, 4)
			ctx := context.Background()
			s := newQuotaSeeder(t, pg, "docker-delete", "docker")
			layer := "sha256:" + strings.Repeat("aa", 32)

			tt.setup(t, pg, s.repo, layer)

			got, err := pg.DeleteDockerBlobFromImage(ctx, s.repo, tt.image, layer)
			if err != nil {
				t.Fatalf("DeleteDockerBlobFromImage: %v", err)
			}

			if got != tt.want {
				t.Fatalf("result = %d, want %d", got, tt.want)
			}

			blob, err := pg.GetDockerBlob(ctx, s.repo, layer)
			if err != nil {
				t.Fatal(err)
			}

			if (blob != nil) != tt.rowKept {
				t.Fatalf("record kept = %v, want %v", blob != nil, tt.rowKept)
			}

			if !tt.rowKept {
				return
			}

			for image, want := range map[string]bool{imageA: tt.visibleToA, imageB: tt.visibleToB} {
				v, err := pg.DockerBlobVisible(ctx, s.repo, image, layer)
				if err != nil {
					t.Fatal(err)
				}

				if v != want {
					t.Errorf("blob visible to %s = %v, want %v", image, v, want)
				}
			}
		})
	}
}

func TestDeleteDockerBlobFromImageMissingRecord(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	s := newQuotaSeeder(t, pg, "docker-delete-gone", "docker")

	_, err := pg.DeleteDockerBlobFromImage(context.Background(), s.repo, "app", "sha256:"+strings.Repeat("bb", 32))
	if !errors.Is(err, ErrDockerRowNotFound) {
		t.Fatalf("delete of an absent blob = %v, want ErrDockerRowNotFound", err)
	}
}

func pushDockerBlob(t *testing.T, pg *PostgresDB, repo, digest, image string) {
	t.Helper()

	sha := strings.TrimPrefix(digest, "sha256:")
	if err := pg.CreateDockerBlob(context.Background(), &models.DockerBlob{
		RepositoryID: repo, Digest: digest, BlobSHA256: sha, Size: 7, Image: &image,
	}); err != nil {
		t.Fatalf("CreateDockerBlob: %v", err)
	}
}

func linkDockerBlob(t *testing.T, pg *PostgresDB, repo, digest, image string) {
	t.Helper()

	if err := pg.LinkDockerBlob(context.Background(), repo, image, digest); err != nil {
		t.Fatalf("LinkDockerBlob: %v", err)
	}
}

// deleteDockerBlobLinks turns the blob into one that predates image links.
func deleteDockerBlobLinks(t *testing.T, pg *PostgresDB, repo, digest string) {
	t.Helper()

	if _, err := pg.GetDB().ExecContext(context.Background(),
		`DELETE FROM docker_blob_images WHERE repository_id = $1 AND digest = $2`, repo, digest); err != nil {
		t.Fatalf("delete blob links: %v", err)
	}
}

// pushDockerManifest stores an indexed manifest of the image referencing the
// layer.
func pushDockerManifest(t *testing.T, pg *PostgresDB, repo, image, digest, layer string) {
	t.Helper()

	sha := strings.TrimPrefix(digest, "sha256:")

	blobID, err := pg.upsertBlobWithMediaType(context.Background(), sha, sha, "application/vnd.oci.image.manifest.v1+json", 10)
	if err != nil {
		t.Fatalf("upsert manifest blob: %v", err)
	}

	if err := pg.CreateDockerManifest(context.Background(), &models.DockerManifest{
		RepositoryID: repo, Image: image, Digest: digest, MediaType: "application/vnd.oci.image.manifest.v1+json",
		BlobID: blobID, Size: 10, Refs: &models.DockerManifestRefs{Blobs: []string{layer}},
	}); err != nil {
		t.Fatalf("CreateDockerManifest: %v", err)
	}
}
