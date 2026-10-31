// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestRepositoryMinAgeRoundTrip checks min_age_hours survives create, read and
// update, and that the CHECK constraint rejects values out of range.
func TestRepositoryMinAgeRoundTrip(t *testing.T) {
	ctx := t.Context()
	orgID := systemOrgID
	repo := &models.Repository{
		Name: "min-age-px", Format: "npm", RepoType: "proxy", UpstreamURL: "https://registry.example.com",
		CacheTTLSeconds: 3600, NegativeCacheTTLSeconds: 60, MaxStaleSeconds: 60, MinAgeHours: 72, ProjectID: &orgID,
	}

	if err := harness.Database.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	got, err := harness.Database.GetRepositoryByName(ctx, repo.Name)
	if err != nil || got == nil || got.MinAgeHours != 72 {
		t.Fatalf("after create: %+v, %v; want MinAgeHours 72", got, err)
	}

	got.MinAgeHours = 0
	if err := harness.Database.UpdateRepository(ctx, got); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	got, err = harness.Database.GetRepositoryByName(ctx, repo.Name)
	if err != nil || got == nil || got.MinAgeHours != 0 {
		t.Fatalf("after update: %+v, %v; want MinAgeHours 0", got, err)
	}

	got.MinAgeHours = 87601
	if err := harness.Database.UpdateRepository(ctx, got); err == nil {
		t.Error("UpdateRepository accepted min_age_hours above 87600")
	}
}
