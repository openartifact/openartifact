// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TestAuthCacheInvalidatedByWrites proves that every write which changes what a
// request is allowed to do is visible to the very next lookup, even though the
// cache TTL (a minute) is far longer than the test: stale reads would fail it.
type authCacheFixture struct {
	pg   *database.PostgresDB
	user *models.User
	tok  *models.Token
}

// newAuthCacheFixture opens a database whose cache TTL (a minute) is far
// longer than any test: a stale read would fail, expiry never masks it.
func newAuthCacheFixture(t *testing.T) authCacheFixture {
	t.Helper()

	pg := freshPostgresCfg(t, 5, func(c *database.DatabaseConfig) { c.AuthCacheTTLMillis = 60_000 })
	ctx := context.Background()

	user, err := pg.CreateUser(ctx, &models.User{Username: "cache-user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	tok, err := pg.CreateToken(ctx, &models.Token{UserID: user.ID, Name: "t", Kind: "access", TokenHash: "cache-hash-1", Scopes: []string{"read"}})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	return authCacheFixture{pg: pg, user: user, tok: tok}
}

func TestAuthCache_TokenRevokeVisible(t *testing.T) {
	f := newAuthCacheFixture(t)
	ctx := context.Background()

	got, err := f.pg.GetTokenByHash(ctx, "cache-hash-1") // warms the cache
	if err != nil || got == nil || got.RevokedAt != nil {
		t.Fatalf("warm lookup = %+v, %v", got, err)
	}

	if err := f.pg.RevokeToken(ctx, f.tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	got, err = f.pg.GetTokenByHash(ctx, "cache-hash-1")
	if err != nil || got == nil || got.RevokedAt == nil {
		t.Fatalf("token still looks valid after revoke: %+v, %v", got, err)
	}
}

func TestAuthCache_UserDisableVisible(t *testing.T) {
	f := newAuthCacheFixture(t)
	ctx := context.Background()

	if u, _ := f.pg.GetUserByID(ctx, f.user.ID); u == nil || u.Disabled {
		t.Fatalf("warm lookup = %+v", u)
	}

	f.user.Disabled = true
	if err := f.pg.UpdateUser(ctx, f.user); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	if u, err := f.pg.GetUserByID(ctx, f.user.ID); err != nil || u == nil || !u.Disabled {
		t.Fatalf("user still enabled after disable: %+v, %v", u, err)
	}
}

func TestAuthCache_GroupMembershipVisible(t *testing.T) {
	f := newAuthCacheFixture(t)
	ctx := context.Background()

	grp, err := f.pg.CreateGroup(ctx, &models.Group{Name: "cache-group"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if gs, _ := f.pg.ListGroupsByUser(ctx, f.user.ID); len(gs) != 0 {
		t.Fatalf("unexpected groups %v", gs)
	}

	if err := f.pg.AddUserToGroup(ctx, f.user.ID, grp.ID); err != nil {
		t.Fatalf("AddUserToGroup: %v", err)
	}

	if gs, _ := f.pg.ListGroupsByUser(ctx, f.user.ID); len(gs) != 1 {
		t.Fatalf("group not visible after AddUserToGroup: %v", gs)
	}

	if err := f.pg.RemoveUserFromGroup(ctx, f.user.ID, grp.ID); err != nil {
		t.Fatalf("RemoveUserFromGroup: %v", err)
	}

	if gs, _ := f.pg.ListGroupsByUser(ctx, f.user.ID); len(gs) != 0 {
		t.Fatalf("group still visible after RemoveUserFromGroup: %v", gs)
	}
}

func TestAuthCache_PermissionsVisible(t *testing.T) {
	f := newAuthCacheFixture(t)
	ctx := context.Background()

	pt, err := f.pg.CreatePermissionTarget(ctx, &models.PermissionTarget{Name: "cache-pt", RepositoryFilter: "*", PathPattern: "**", Actions: []string{"read"}})
	if err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	if ts, _ := f.pg.GetPermissionTargetsForPrincipal(ctx, f.user.ID, nil); len(ts) != 0 {
		t.Fatalf("unexpected targets %v", ts)
	}

	assign := &models.PermissionAssignment{PermissionTargetID: pt.ID, PrincipalType: "user", PrincipalID: f.user.ID}
	if err := f.pg.CreatePermissionAssignment(ctx, assign); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	if ts, _ := f.pg.GetPermissionTargetsForPrincipal(ctx, f.user.ID, nil); len(ts) != 1 {
		t.Fatalf("grant not visible after CreatePermissionAssignment: %v", ts)
	}

	pt.Actions = []string{"read", "write"}
	if err := f.pg.UpdatePermissionTarget(ctx, pt); err != nil {
		t.Fatalf("UpdatePermissionTarget: %v", err)
	}

	if ts, _ := f.pg.GetPermissionTargetsForPrincipal(ctx, f.user.ID, nil); len(ts) != 1 || len(ts[0].Actions) != 2 {
		t.Fatalf("updated actions not visible: %v", ts)
	}

	if err := f.pg.DeletePermissionAssignment(ctx, pt.ID, "user", f.user.ID); err != nil {
		t.Fatalf("DeletePermissionAssignment: %v", err)
	}

	if ts, _ := f.pg.GetPermissionTargetsForPrincipal(ctx, f.user.ID, nil); len(ts) != 0 {
		t.Fatalf("revoked grant still visible: %v", ts)
	}
}

func TestAuthCache_RepositoryChangesVisible(t *testing.T) {
	f := newAuthCacheFixture(t)
	ctx := context.Background()
	systemOrg := "00000000-0000-0000-0000-000000000001"

	repo := &models.Repository{Name: "cache-repo", Format: "raw", RepoType: "hosted", OrgID: &systemOrg}
	if err := f.pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	got, err := f.pg.GetRepositoryByName(ctx, "cache-repo")
	if err != nil || got == nil || got.Public {
		t.Fatalf("warm lookup = %+v, %v", got, err)
	}

	got.Public = true
	if err := f.pg.UpdateRepository(ctx, got); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	if again, _ := f.pg.GetRepositoryByName(ctx, "cache-repo"); again == nil || !again.Public {
		t.Fatalf("repository still private after update: %+v", again)
	}

	if err := f.pg.DeleteRepository(ctx, got.ID); err != nil {
		t.Fatalf("DeleteRepository: %v", err)
	}

	if again, err := f.pg.GetRepositoryByName(ctx, "cache-repo"); err != nil || again != nil {
		t.Fatalf("deleted repository still served: %+v, %v", again, err)
	}
}
