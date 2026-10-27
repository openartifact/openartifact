// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestTrashRepositoryRestoreKeepsRemainingWindow(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	pg.SetTrashRetentionDays(7)

	ctx := context.Background()
	s := newQuotaSeeder(t, pg, "trash-window", "docker")

	s.maven(s.blob(1)) // maven row in a docker repo is fine for the schema
	mavenID := mavenArtifactID(t, s, "g/a1/1.0/a-1.0.jar")
	manifestID, tagID := seedDockerImage(s, "sha256:bbbb", "v1")

	for _, kind := range []struct {
		k  models.TrashKind
		id string
	}{{models.TrashKindMaven, mavenID}, {models.TrashKindDockerManifest, manifestID}} {
		if soft, err := pg.TrashRows(ctx, s.repo, kind.k, []string{kind.id}, "bob"); err != nil || !soft {
			t.Fatalf("TrashRows %s = %v, %v", kind.k, soft, err)
		}
	}

	// The maven item was trashed 3 days ago, the manifest (and its tag) 3 days
	// ago too; the repository 2 days ago.
	s.exec(`UPDATE maven_artifacts SET deleted_at = now() - interval '3 days' WHERE id = $1`, mavenID)
	// One fixed instant keeps the cascade pair equal, as the real cascade does.
	s.exec(`WITH ts AS (SELECT now() - interval '3 days' AS t),
	        m AS (UPDATE docker_manifests SET deleted_at = (SELECT t FROM ts) WHERE id = $1)
	        UPDATE docker_tags SET deleted_at = (SELECT t FROM ts) WHERE id = $2`, manifestID, tagID)

	if soft, err := pg.TrashRepository(ctx, s.repo, "carol"); err != nil || !soft {
		t.Fatalf("TrashRepository = %v, %v", soft, err)
	}

	s.exec(`UPDATE repositories SET deleted_at = now() - interval '2 days' WHERE id = $1`, s.repo)

	if _, err := pg.RestoreTrash(ctx, models.TrashKindRepository, s.repo); err != nil {
		t.Fatal(err)
	}

	// Each item was trashed 1 day before the repository: it is now 1 day old.
	var age time.Duration

	var ageSecs float64
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT EXTRACT(EPOCH FROM now() - deleted_at) FROM maven_artifacts WHERE id = $1`, mavenID).Scan(&ageSecs); err != nil {
		t.Fatal(err)
	}

	age = time.Duration(ageSecs * float64(time.Second))
	if age < 23*time.Hour || age > 25*time.Hour {
		t.Fatalf("maven item age after repo restore = %v, want ~24h", age)
	}

	expired, err := pg.ListExpiredTrash(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}

	if len(expired) != 0 {
		t.Fatalf("items expired right after the repository restore: %+v", expired)
	}

	// Manifest and tag keep identical timestamps, so they restore together.
	var equal bool
	if err := pg.GetDB().QueryRowContext(ctx,
		`SELECT m.deleted_at = t.deleted_at FROM docker_manifests m, docker_tags t WHERE m.id = $1 AND t.id = $2`,
		manifestID, tagID).Scan(&equal); err != nil || !equal {
		t.Fatalf("manifest/tag deleted_at equal = %v, %v", equal, err)
	}

	if _, err := pg.RestoreTrash(ctx, models.TrashKindDockerManifest, manifestID); err != nil {
		t.Fatal(err)
	}

	var live int
	if err := pg.GetDB().
		QueryRowContext(ctx, `SELECT COUNT(*) FROM docker_tags WHERE id = $1 AND deleted_at IS NULL`, tagID).
		Scan(&live); err != nil ||
		live != 1 {
		t.Fatalf("tag not restored with its manifest: %d, %v", live, err)
	}
}
