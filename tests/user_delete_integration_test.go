// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

func rowCount(t *testing.T, pg *database.PostgresDB, q string, args ...any) int {
	t.Helper()

	var n int
	if err := pg.GetDB().QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}

	return n
}

// seedAdmins makes userID an admin plus a second admin, so the last-admin guard
// does not interfere with the delete under test.
func seedAdmins(t *testing.T, pg *database.PostgresDB, userID string) {
	t.Helper()

	ctx := context.Background()

	pt, err := pg.GetPermissionTargetByName(ctx, "admin-all")
	if err != nil || pt == nil {
		t.Fatalf("admin-all target: %v", err)
	}

	other, err := pg.CreateUser(ctx, &models.User{Username: "other-admin"})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{userID, other.ID} {
		if err := pg.GetOrCreatePermissionAssignment(ctx, pt.ID, "user", id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresDeleteUser_RemovesEverythingAndSSOReprovisions(t *testing.T) {
	pg := freshPostgres(t, 4)
	ctx := context.Background()

	provider := &models.SSOProvider{ID: "p1", Slug: "okta", Kind: "oidc", AutoProvisionUsers: true}
	prov := auth.NewProvisioner(pg, newTestLogger(t))
	claims := auth.SSOClaims{ExternalID: "ext-1", ProviderSlug: "okta", Username: "alice", Email: "alice@example.com", EmailVerified: true}

	first, err := prov.Provision(ctx, provider, claims)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	if _, err := pg.CreateSession(ctx, &models.Session{UserID: first.ID, TokenHash: "sh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.CreateToken(ctx, &models.Token{UserID: first.ID, Name: "t", TokenHash: "th", Kind: "access"}); err != nil {
		t.Fatal(err)
	}

	grp, err := pg.GetOrCreateGroup(ctx, "devs")
	if err != nil {
		t.Fatal(err)
	}

	if err := pg.AddUserToGroup(ctx, first.ID, grp.ID); err != nil {
		t.Fatal(err)
	}

	proj := &models.Project{Name: "p", Slug: "p"}
	if err := pg.CreateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}

	if err := pg.AddProjectMember(ctx, proj.ID, first.ID, "developer"); err != nil {
		t.Fatal(err)
	}

	seedAdmins(t, pg, first.ID)

	ok, err := pg.DeleteUser(ctx, first.ID)
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}

	for _, q := range []string{
		`SELECT count(*) FROM auth_users WHERE id=$1`,
		`SELECT count(*) FROM auth_sessions WHERE user_id=$1`,
		`SELECT count(*) FROM auth_tokens WHERE user_id=$1`,
		`SELECT count(*) FROM auth_user_groups WHERE user_id=$1`,
		`SELECT count(*) FROM project_members WHERE user_id=$1`,
		`SELECT count(*) FROM permission_assignments WHERE principal_id=$1`,
	} {
		if n := rowCount(t, pg, q, first.ID); n != 0 {
			t.Errorf("%s: %d rows left", q, n)
		}
	}

	if u, _ := pg.GetUserByExternalID(ctx, "ext-1", "okta"); u != nil {
		t.Fatal("SSO identity still resolves after delete")
	}

	second, err := prov.Provision(ctx, provider, claims)
	if err != nil {
		t.Fatalf("re-provision: %v", err)
	}

	if second.ID == first.ID {
		t.Error("re-login must provision a fresh user, got the old id")
	}

	if ok, err := pg.DeleteUser(ctx, first.ID); err != nil || ok {
		t.Errorf("second delete: ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestPostgresDeleteUser_LastAdminAndService(t *testing.T) {
	pg := freshPostgres(t, 4)
	ctx := context.Background()

	pt, err := pg.GetPermissionTargetByName(ctx, "admin-all")
	if err != nil || pt == nil {
		t.Fatalf("admin-all target: %v", err)
	}

	mk := func(name string, disabled bool) *models.User {
		u, err := pg.CreateUser(ctx, &models.User{Username: name, Disabled: disabled})
		if err != nil {
			t.Fatal(err)
		}

		if err := pg.GetOrCreatePermissionAssignment(ctx, pt.ID, "user", u.ID); err != nil {
			t.Fatal(err)
		}

		return u
	}

	enabled := mk("enabled-admin", false)
	disabled := mk("disabled-admin", true)

	// Only one enabled admin: deleting it is refused, a disabled admin is not counted.
	if _, err := pg.DeleteUser(ctx, enabled.ID); !errors.Is(err, database.ErrLastAdmin) {
		t.Fatalf("want ErrLastAdmin, got %v", err)
	}

	if ok, err := pg.DeleteUser(ctx, disabled.ID); err != nil || !ok {
		t.Fatalf("delete disabled admin: ok=%v err=%v", ok, err)
	}

	// Admin through a group counts.
	viaGroup, err := pg.CreateUser(ctx, &models.User{Username: "group-admin"})
	if err != nil {
		t.Fatal(err)
	}

	grp, _ := pg.GetOrCreateGroup(ctx, "admins")
	if err := pg.AddUserToGroup(ctx, viaGroup.ID, grp.ID); err != nil {
		t.Fatal(err)
	}

	if err := pg.GetOrCreatePermissionAssignment(ctx, pt.ID, "group", grp.ID); err != nil {
		t.Fatal(err)
	}

	if ok, err := pg.DeleteUser(ctx, enabled.ID); err != nil || !ok {
		t.Fatalf("delete with a group admin remaining: ok=%v err=%v", ok, err)
	}

	if _, err := pg.DeleteUser(ctx, viaGroup.ID); !errors.Is(err, database.ErrLastAdmin) {
		t.Fatalf("group admin is now last: want ErrLastAdmin, got %v", err)
	}
}
