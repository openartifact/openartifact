// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// merge_proxy_versions defaults to false (hosted wins) and round-trips through
// create, update and the group-member-creating transaction.
func TestRepositoryMergeProxyVersionsRoundTrip(t *testing.T) {
	db := newQuotaTestDB(t, 4)
	ctx := context.Background()
	sys := models.SystemProjectID

	plain := &models.Repository{Name: "g-default", Format: "npm", RepoType: "group", ProjectID: &sys}
	if err := db.CreateRepository(ctx, plain); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetRepositoryByNameUncached(ctx, "g-default")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}

	if got.MergeProxyVersions {
		t.Fatal("a new group must default to hosted-wins")
	}

	got.MergeProxyVersions = true
	if err := db.UpdateRepository(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ = db.GetRepositoryByNameUncached(ctx, "g-default")
	if got == nil || !got.MergeProxyVersions {
		t.Fatalf("update did not persist merge_proxy_versions: %+v", got)
	}

	merged := &models.Repository{Name: "g-merged", Format: "npm", RepoType: "group", ProjectID: &sys, MergeProxyVersions: true}
	if err := db.CreateRepositoryWithMembers(ctx, merged, nil); err != nil {
		t.Fatalf("create with members: %v", err)
	}

	got, _ = db.GetRepositoryByNameUncached(ctx, "g-merged")
	if got == nil || !got.MergeProxyVersions {
		t.Fatalf("create did not persist merge_proxy_versions: %+v", got)
	}
}
