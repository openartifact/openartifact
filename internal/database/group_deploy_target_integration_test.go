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

// The deploy target of a group is read back with the repository, is unset when
// the member leaves the group or its repository is deleted, and a trashed
// target stops being one.
func TestGroupDeployTargetLifecycle(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	org := systemOrgID

	mk := func(name, repoType string) *models.Repository {
		r := &models.Repository{Name: name, Format: "raw", RepoType: repoType, ProjectID: &org}
		if err := pg.CreateRepository(ctx, r); err != nil {
			t.Fatalf("CreateRepository %s: %v", name, err)
		}

		return r
	}

	grp, a, b := mk("dt-grp", "group"), mk("dt-a", "hosted"), mk("dt-b", "hosted")

	if err := pg.ReplaceGroupMembers(ctx, grp.ID, []string{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}

	setTarget := func(id *string) {
		t.Helper()

		err := pg.UpdateRepositorySettings(ctx, grp, RepositorySettingsChange{SetDeployTarget: true, DeployTargetID: id})
		if err != nil {
			t.Fatalf("UpdateRepositorySettings: %v", err)
		}
	}

	target := func() string {
		t.Helper()

		got, err := pg.GetRepositoryByName(ctx, "dt-grp")
		if err != nil || got == nil {
			t.Fatalf("GetRepositoryByName: %v %v", got, err)
		}

		return got.DeployTarget
	}

	if got := target(); got != "" {
		t.Fatalf("a new group has deploy target %q", got)
	}

	setTarget(&a.ID)

	if got := target(); got != "dt-a" {
		t.Fatalf("deploy target = %q, want dt-a", got)
	}

	// An unrelated update of the group keeps it.
	grp.Description = "changed"
	if err := pg.UpdateRepository(ctx, grp); err != nil {
		t.Fatal(err)
	}

	if got := target(); got != "dt-a" {
		t.Fatalf("after a settings update the target = %q, want dt-a", got)
	}

	// The member leaves the group: no target.
	if err := pg.ReplaceGroupMembers(ctx, grp.ID, []string{b.ID}); err != nil {
		t.Fatal(err)
	}

	if got := target(); got != "" {
		t.Fatalf("after the target left the group it is still %q", got)
	}

	// Replacing the members while keeping the target keeps it.
	if err := pg.ReplaceGroupMembers(ctx, grp.ID, []string{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}

	setTarget(&b.ID)

	if err := pg.ReplaceGroupMembers(ctx, grp.ID, []string{b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}

	if got := target(); got != "dt-b" {
		t.Fatalf("reordering the members lost the target: %q", got)
	}

	deployTargetTrashAndDelete(t, pg, grp, a, b, target, setTarget)
}

func deployTargetTrashAndDelete(
	t *testing.T,
	pg *PostgresDB,
	grp, a, b *models.Repository,
	target func() string,
	setTarget func(*string),
) {
	t.Helper()

	ctx := context.Background()

	// The restore of a backup points a group at a member; a non-member is ignored.
	if err := pg.SetGroupDeployTarget(ctx, grp.ID, a.ID); err != nil {
		t.Fatal(err)
	}

	if got := target(); got != "dt-a" {
		t.Fatalf("SetGroupDeployTarget left %q, want dt-a", got)
	}

	if err := pg.SetGroupDeployTarget(ctx, grp.ID, grp.ID); err != nil {
		t.Fatal(err)
	}

	if got := target(); got != "dt-a" {
		t.Fatalf("a non-member became the target: %q", got)
	}

	setTarget(&b.ID)

	// A trashed target is no target; restoring it is not needed for the group to keep answering.
	if _, err := pg.GetDB().ExecContext(ctx, `UPDATE repositories SET deleted_at = now() WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}

	pg.authCache.invalidate()

	if got := target(); got != "" {
		t.Fatalf("a trashed target is still reported: %q", got)
	}

	// Deleting the target's repository for good unsets the column.
	if err := pg.DeleteRepository(ctx, b.ID); err != nil {
		t.Fatal(err)
	}

	var raw *string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT deploy_target_id FROM repositories WHERE id = $1`, grp.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}

	if raw != nil {
		t.Fatalf("deploy_target_id = %s after the target was deleted, want NULL", *raw)
	}
}

// A group cannot be its own deploy target.
func TestGroupDeployTargetNotSelf(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	org := systemOrgID

	grp := &models.Repository{Name: "dt-self", Format: "raw", RepoType: "group", ProjectID: &org}
	if err := pg.CreateRepository(ctx, grp); err != nil {
		t.Fatal(err)
	}

	err := pg.UpdateRepositorySettings(ctx, grp, RepositorySettingsChange{SetDeployTarget: true, DeployTargetID: &grp.ID})
	if err == nil {
		t.Fatal("a group became its own deploy target")
	}
}
