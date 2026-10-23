// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// A sealed bundle must keep its items: the conditional delete refuses it.
func TestRemoveBundleItemRefusesSealedBundle(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repoID := newQuotaRepo(t, pg, "bundle-src", 0, 0)

	b := &models.ReleaseBundle{Name: "shop", Version: "1.0.0", State: models.BundleStateDraft, CreatedBy: "admin"}
	if err := pg.CreateReleaseBundle(ctx, b); err != nil {
		t.Fatalf("CreateReleaseBundle: %v", err)
	}

	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	item := &models.ReleaseBundleItem{
		BundleID: b.ID, SourceRepoID: repoID, SHA256: sha, ArtifactPath: "a/b.bin", Format: "raw", SizeBytes: 1, AddedAt: time.Now(),
	}

	if err := pg.AddBundleItems(ctx, b.ID, []*models.ReleaseBundleItem{item}); err != nil {
		t.Fatalf("AddBundleItems: %v", err)
	}

	if ok, err := pg.SealReleaseBundle(ctx, b.ID, "admin", time.Now(), nil); err != nil || !ok {
		t.Fatalf("SealReleaseBundle = %v, %v", ok, err)
	}

	if err := pg.RemoveBundleItem(ctx, b.ID, sha); !errors.Is(err, ErrBundleNotDraft) {
		t.Fatalf("RemoveBundleItem on sealed bundle = %v, want ErrBundleNotDraft", err)
	}

	if got, err := pg.GetBundleItem(ctx, b.ID, sha); err != nil || got == nil {
		t.Fatalf("item must survive: %v, %v", got, err)
	}
}

// Members kept while trashed must not share priorities with a new list.
func TestReplaceGroupMembersRenumbersKeptMembers(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	org := systemOrgID

	mk := func(name, repoType string) string {
		r := &models.Repository{Name: name, Format: "raw", RepoType: repoType, ProjectID: &org}
		if err := pg.CreateRepository(ctx, r); err != nil {
			t.Fatalf("CreateRepository %s: %v", name, err)
		}

		return r.ID
	}

	group, a, b, c, d := mk("grp", "group"), mk("m-a", "hosted"), mk("m-b", "hosted"), mk("m-c", "hosted"), mk("m-d", "hosted")

	if err := pg.ReplaceGroupMembers(ctx, group, []string{a, b}); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE repositories SET deleted_at = now() WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}

	if err := pg.ReplaceGroupMembers(ctx, group, []string{c, d}); err != nil {
		t.Fatal(err)
	}

	rows, err := pg.GetDB().QueryContext(ctx, `SELECT priority FROM repository_group_members WHERE group_repo_id = $1`, group)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	seen := map[int]bool{}

	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}

		if seen[p] {
			t.Fatalf("duplicate priority %d", p)
		}

		seen[p] = true
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 3 {
		t.Fatalf("want 3 distinct priorities (c, d, kept b), got %v", seen)
	}
}
