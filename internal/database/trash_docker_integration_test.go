// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// seedDockerImage inserts a manifest and a tag pointing at it, returning both ids.
func seedDockerImage(s *quotaSeeder, digest, tag string) (string, string) {
	s.tb.Helper()

	manifestID := s.queryID(`INSERT INTO docker_manifests (repository_id, image, digest, media_type, blob_id)
	        VALUES ($1, 'app', $2, 'application/vnd.oci.image.manifest.v1+json', $3) RETURNING id`,
		s.repo, digest, s.blob(10))
	tagID := s.queryID(`INSERT INTO docker_tags (repository_id, image, name, manifest_digest)
	        VALUES ($1, 'app', $2, $3) RETURNING id`, s.repo, tag, digest)

	return manifestID, tagID
}

func TestTrashDockerTagRestoreNeedsLiveManifest(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-dtag", "docker")
	manifestID, tagID := seedDockerImage(s, "sha256:aaaa", "v1")

	// Trashing the manifest takes its tag along (same deleted_at).
	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindDockerManifest, []string{manifestID}, "bob"); err != nil || !soft {
		t.Fatalf("TrashRows manifest = %v, %v", soft, err)
	}

	// The cascaded tag cannot come back alone.
	if _, err := pg.RestoreTrash(ctx, models.TrashKindDockerTag, tagID); !errors.Is(err, ErrTrashManifestInTrash) {
		t.Fatalf("restore tag of trashed manifest = %v, want ErrTrashManifestInTrash", err)
	}

	// Restoring the manifest revives the tag.
	if _, err := pg.RestoreTrash(ctx, models.TrashKindDockerManifest, manifestID); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT COUNT(*) FROM docker_tags WHERE id = $1 AND deleted_at IS NULL`, tagID).
		Scan(&n); err != nil ||
		n != 1 {
		t.Fatalf("tag live after manifest restore: %d, %v", n, err)
	}

	// A tag trashed on its own restores while its manifest is live.
	if soft, err := pg.TrashRows(ctx, s.repo, models.TrashKindDockerTag, []string{tagID}, "bob"); err != nil || !soft {
		t.Fatalf("TrashRows tag = %v, %v", soft, err)
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindDockerTag, tagID); err != nil {
		t.Fatalf("restore tag with live manifest = %v", err)
	}
}
