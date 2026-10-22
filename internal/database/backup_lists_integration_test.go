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

type aclSeed struct {
	user   *models.User
	group  *models.Group
	target *models.PermissionTarget
	repo   string
}

func seedACL(t *testing.T, pg *PostgresDB, s *quotaSeeder) aclSeed {
	t.Helper()

	ctx := context.Background()
	email, hash := "lister@example.test", "x"

	user, err := pg.CreateUser(ctx, &models.User{Username: "lister", Email: &email, PasswordHash: &hash})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	group, err := pg.CreateGroup(ctx, &models.Group{Name: "listers"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	target, err := pg.CreatePermissionTarget(ctx, &models.PermissionTarget{Name: "lists", RepositoryFilter: "*", PathPattern: "**", Actions: []string{"read"}})
	if err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	for _, p := range [][2]string{{"user", user.ID}, {"group", group.ID}} {
		if err := pg.GetOrCreatePermissionAssignment(ctx, target.ID, p[0], p[1]); err != nil {
			t.Fatalf("assign %s: %v", p[0], err)
		}
	}

	if err := pg.AddUserToGroup(ctx, user.ID, group.ID); err != nil {
		t.Fatalf("AddUserToGroup: %v", err)
	}

	s.exec(`UPDATE auth_user_groups SET is_sso_managed = true WHERE user_id = $1 AND group_id = $2`, user.ID, group.ID)

	if _, err := pg.GetOrCreateStorageQuota(ctx, s.repo); err != nil {
		t.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	if err := pg.UpdateStorageQuota(ctx, &models.StorageQuota{RepositoryID: s.repo, MaxSizeBytes: 4096, MaxArtifactCount: 7, WarnThresholdPercent: 65}); err != nil {
		t.Fatalf("UpdateStorageQuota: %v", err)
	}

	return aclSeed{user: user, group: group, target: target, repo: s.repo}
}

func assertAssignmentsListed(t *testing.T, pg *PostgresDB, seed aclSeed) {
	t.Helper()

	assigns, err := pg.ListPermissionAssignments(context.Background())
	if err != nil {
		t.Fatalf("ListPermissionAssignments: %v", err)
	}

	got := map[string]string{}

	for _, a := range assigns {
		if a.PermissionTargetID == seed.target.ID {
			got[a.PrincipalType] = a.PrincipalID
		}
	}

	if got["user"] != seed.user.ID || got["group"] != seed.group.ID {
		t.Fatalf("assignments for the target = %v, want the user and the group", got)
	}
}

func assertMembershipListed(t *testing.T, pg *PostgresDB, seed aclSeed) {
	t.Helper()

	members, err := pg.ListUserGroupMemberships(context.Background())
	if err != nil {
		t.Fatalf("ListUserGroupMemberships: %v", err)
	}

	for _, m := range members {
		if m.UserID == seed.user.ID && m.GroupID == seed.group.ID {
			if !m.SSOManaged {
				t.Error("membership lost its SSO-managed flag")
			}

			return
		}
	}

	t.Fatalf("membership missing from %+v", members)
}

func assertQuotaListed(t *testing.T, pg *PostgresDB, seed aclSeed) {
	t.Helper()

	quotas, err := pg.ListStorageQuotas(context.Background())
	if err != nil {
		t.Fatalf("ListStorageQuotas: %v", err)
	}

	for _, q := range quotas {
		if q.RepositoryID == seed.repo {
			if q.MaxSizeBytes != 4096 || q.MaxArtifactCount != 7 || q.WarnThresholdPercent != 65 {
				t.Fatalf("quota = %+v, want 4096 / 7 / 65", q)
			}

			return
		}
	}

	t.Fatalf("quota of %s missing from %+v", seed.repo, quotas)
}

// The bulk readers feeding the backup must return what the targeted writers
// stored: assignments of both principal kinds, memberships with their SSO flag,
// and the quota limits.
func TestBackupListsReturnAccessControlAndQuotas(t *testing.T) {
	pg := newQuotaTestDB(t, 8)
	seed := seedACL(t, pg, newQuotaSeeder(t, pg, "backup-lists", "raw"))

	assertAssignmentsListed(t, pg, seed)
	assertMembershipListed(t, pg, seed)
	assertQuotaListed(t, pg, seed)
}
