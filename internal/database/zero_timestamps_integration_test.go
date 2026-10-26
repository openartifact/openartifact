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

	"github.com/openartifact/openartifact/internal/database/models"
)

// requireStamped fails when a stored timestamp is the zero time (the symptom
// of an upsert that persisted an unset time.Time) or far from now.
func requireStamped(t *testing.T, what string, got, before time.Time) {
	t.Helper()

	if got.IsZero() || got.Year() < 2000 {
		t.Errorf("%s = %v: the zero time was persisted", what, got)

		return
	}

	if got.Before(before.Add(-time.Minute)) || got.After(time.Now().Add(time.Minute)) {
		t.Errorf("%s = %v, want about now", what, got)
	}
}

// Writers that take a caller-supplied time must default an unset one to now
// rather than store 0001-01-01.
func TestWritersDefaultUnsetTimestampsToNow(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repoID := newQuotaRepo(t, pg, "zero-ts", 0, 0)
	before := time.Now()

	t.Run("backup job", func(t *testing.T) {
		job := &models.BackupJob{Status: "pending", Type: "full"}
		if err := pg.CreateBackupJob(ctx, job); err != nil {
			t.Fatal(err)
		}

		got, err := pg.GetBackupJob(ctx, job.ID)
		if err != nil || got == nil {
			t.Fatalf("GetBackupJob = %v, %v", got, err)
		}

		requireStamped(t, "created_at", got.CreatedAt, before)
		requireStamped(t, "updated_at", got.UpdatedAt, before)
	})

	t.Run("license policy", func(t *testing.T) {
		policy := &models.LicensePolicy{RepositoryID: &repoID, Name: "p", Action: "warn", Enabled: true}
		if err := pg.CreateLicensePolicy(ctx, policy); err != nil {
			t.Fatal(err)
		}

		got, err := pg.GetLicensePolicy(ctx, policy.ID)
		if err != nil || got == nil {
			t.Fatalf("GetLicensePolicy = %v, %v", got, err)
		}

		requireStamped(t, "created_at", got.CreatedAt, before)
		requireStamped(t, "updated_at", got.UpdatedAt, before)
	})

	t.Run("storage backend", func(t *testing.T) {
		backend := &models.StorageBackend{ID: "00000000-0000-0000-0000-0000000000b1", Name: "zero-ts", Kind: "filesystem", Config: []byte(`{}`), Status: "unknown"}
		if err := pg.CreateStorageBackend(ctx, backend); err != nil {
			t.Fatal(err)
		}

		got, err := pg.GetStorageBackend(ctx, "zero-ts")
		if err != nil || got == nil {
			t.Fatalf("GetStorageBackend = %v, %v", got, err)
		}

		requireStamped(t, "created_at", got.CreatedAt, before)
		requireStamped(t, "updated_at", got.UpdatedAt, before)
	})

	t.Run("bundle items", func(t *testing.T) {
		b := &models.ReleaseBundle{Name: "zero-ts", Version: "1.0.0", State: models.BundleStateDraft, CreatedBy: "admin"}
		if err := pg.CreateReleaseBundle(ctx, b); err != nil {
			t.Fatal(err)
		}

		items := []*models.ReleaseBundleItem{
			{SourceRepoID: repoID, SHA256: strings.Repeat("a1", 32), ArtifactPath: "one.bin", Format: "raw", SizeBytes: 1},
		}
		if err := pg.AddBundleItems(ctx, b.ID, items); err != nil {
			t.Fatal(err)
		}

		single := &models.ReleaseBundleItem{
			ID: "00000000-0000-0000-0000-0000000000c1", BundleID: b.ID, SourceRepoID: repoID,
			SHA256: strings.Repeat("b2", 32), ArtifactPath: "two.bin", Format: "raw", SizeBytes: 2,
		}
		if err := pg.AddBundleItem(ctx, single); err != nil {
			t.Fatal(err)
		}

		got, err := pg.ListBundleItems(ctx, b.ID)
		if err != nil || len(got) != 2 {
			t.Fatalf("ListBundleItems = %d items, %v; want 2", len(got), err)
		}

		for _, it := range got {
			requireStamped(t, "added_at of "+it.ArtifactPath, it.AddedAt, before)
		}
	})
}
