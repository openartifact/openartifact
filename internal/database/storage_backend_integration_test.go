// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
)

// A backend nobody has tested reports no last-check time (nil), not the Unix
// epoch, and the first status update stamps it.
func TestStorageBackendLastCheckedAt(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	ctx := context.Background()
	now := time.Now()

	b := &models.StorageBackend{
		ID: uuid.New().String(), Name: "never-checked", Kind: "filesystem", Config: []byte(`{"path":"/tmp/nc"}`),
		Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}
	if err := pg.CreateStorageBackend(ctx, b); err != nil {
		t.Fatalf("CreateStorageBackend: %v", err)
	}

	got, err := pg.GetStorageBackend(ctx, b.Name)
	if err != nil || got == nil {
		t.Fatalf("GetStorageBackend = %v, %v", got, err)
	}

	if got.LastCheckedAt != nil {
		t.Errorf("Get: LastCheckedAt = %v, want nil before any check", got.LastCheckedAt)
	}

	list, err := pg.ListStorageBackends(ctx)
	if err != nil {
		t.Fatalf("ListStorageBackends: %v", err)
	}

	for _, l := range list {
		if l.Name == b.Name && l.LastCheckedAt != nil {
			t.Errorf("List: LastCheckedAt = %v, want nil before any check", l.LastCheckedAt)
		}
	}

	if err := pg.UpdateStorageBackendStatus(ctx, b.ID, "healthy", nil); err != nil {
		t.Fatalf("UpdateStorageBackendStatus: %v", err)
	}

	got, _ = pg.GetStorageBackend(ctx, b.Name)
	if got.LastCheckedAt == nil || time.Since(*got.LastCheckedAt) > time.Minute {
		t.Errorf("after a check LastCheckedAt = %v, want about now", got.LastCheckedAt)
	}
}
