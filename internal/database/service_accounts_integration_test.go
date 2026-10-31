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

func newServiceAccount(t *testing.T, pg *PostgresDB, name string) *models.User {
	t.Helper()

	desc := "account " + name

	sa, err := pg.CreateServiceAccount(context.Background(), name, &desc, false)
	if err != nil {
		t.Fatalf("CreateServiceAccount(%s): %v", name, err)
	}

	return sa
}

//nolint:gocyclo,cyclop // one scenario over a shared fixture; splitting would repeat the seeding
func TestServiceAccounts_KindIsolationAndCacheInvalidation(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	sa := newServiceAccount(t, pg, "ci-publisher")

	if sa.Kind != models.UserKindServiceAccount || !sa.IsServiceAccount() || sa.IsHuman() || sa.IsService() {
		t.Fatalf("service account = kind %q", sa.Kind)
	}

	if _, err := pg.CreateServiceAccount(ctx, "ci-publisher", nil, false); !IsUniqueViolation(err) {
		t.Fatalf("duplicate name: %v, want a unique violation", err)
	}

	human, err := pg.CreateUser(ctx, &models.User{Username: "alice", Kind: models.UserKindHuman})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// The users API lists humans only; the service accounts API lists service accounts only.
	users, total, err := pg.ListUsers(ctx, 1, 50)
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range users {
		if u.ID == sa.ID {
			t.Fatal("ListUsers returned a service account")
		}
	}

	if total != len(users) {
		t.Fatalf("ListUsers total %d for %d rows", total, len(users))
	}

	accounts, atotal, err := pg.ListServiceAccounts(ctx, 1, 50)
	if err != nil || atotal != 1 || len(accounts) != 1 || accounts[0].ID != sa.ID {
		t.Fatalf("ListServiceAccounts = %v (total %d, err %v)", accounts, atotal, err)
	}

	if got, err := pg.GetServiceAccount(ctx, human.ID); err != nil || got != nil {
		t.Fatalf("GetServiceAccount(human) = %v, %v; want nil", got, err)
	}

	if got, err := pg.UpdateServiceAccount(ctx, human.ID, nil, new(true)); err != nil || got != nil {
		t.Fatalf("UpdateServiceAccount(human) = %v, %v; want nil", got, err)
	}

	if ok, err := pg.DeleteServiceAccount(ctx, human.ID); err != nil || ok {
		t.Fatalf("DeleteServiceAccount(human) = %v, %v; want false", ok, err)
	}

	// Disabling is visible through the cached lookup at once.
	if u, err := pg.GetUserByID(ctx, sa.ID); err != nil || u == nil || u.Disabled {
		t.Fatalf("GetUserByID before = %+v, %v", u, err)
	}

	if _, err := pg.UpdateServiceAccount(ctx, sa.ID, nil, new(true)); err != nil {
		t.Fatal(err)
	}

	if u, err := pg.GetUserByID(ctx, sa.ID); err != nil || u == nil || !u.Disabled {
		t.Fatalf("GetUserByID after disabling = %+v, %v; the auth cache served a stale user", u, err)
	}

	// An absent description keeps it, an empty one clears it.
	if got, err := pg.UpdateServiceAccount(
		ctx,
		sa.ID,
		nil,
		nil,
	); err != nil || got == nil || got.FullName == nil ||
		*got.FullName != "account ci-publisher" {
		t.Fatalf("update without a description = %+v, %v", got, err)
	}

	empty := ""
	if got, err := pg.UpdateServiceAccount(
		ctx,
		sa.ID,
		&empty,
		nil,
	); err != nil || got == nil || got.FullName == nil ||
		*got.FullName != "" {
		t.Fatalf("update with an empty description = %+v, %v", got, err)
	}
}

//nolint:gocyclo,cyclop // one scenario over a shared fixture; splitting would repeat the seeding
func TestServiceAccounts_DeleteRemovesEverythingTheyOwn(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	sa := newServiceAccount(t, pg, "ci-publisher")
	keep := newServiceAccount(t, pg, "ci-other")

	target, err := pg.CreatePermissionTarget(ctx, &models.PermissionTarget{
		Name: "publish", RepositoryFilter: "*", PathPattern: "**", Actions: []string{"read", "write"},
	})
	if err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	for _, id := range []string{sa.ID, keep.ID} {
		if err := pg.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
			PermissionTargetID: target.ID, PrincipalType: "user", PrincipalID: id,
		}); err != nil {
			t.Fatalf("assign: %v", err)
		}
	}

	group, err := pg.CreateGroup(ctx, &models.Group{Name: "ci"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if err := pg.AddUserToGroup(ctx, sa.ID, group.ID); err != nil {
		t.Fatalf("AddUserToGroup: %v", err)
	}

	token, err := pg.CreateToken(ctx, &models.Token{
		UserID: sa.ID, Name: "t", Kind: "deploy", TokenHash: "hash-of-sa-token", Scopes: []string{}, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	policy, err := pg.CreateOIDCPolicy(ctx, &models.OIDCTrustPolicy{
		Name: "gh", Issuer: "https://token.actions.githubusercontent.com", Audience: "a",
		Conditions: []models.OIDCCondition{{Claim: "repository", Patterns: []string{"acme/*"}}}, ServiceAccountID: sa.ID,
		TokenTTLSeconds: 900, Enabled: true, CreatedBy: "admin",
	})
	if err != nil {
		t.Fatalf("CreateOIDCPolicy: %v", err)
	}

	// Prime the caches the delete must invalidate.
	if _, err := pg.GetUserByID(ctx, sa.ID); err != nil {
		t.Fatal(err)
	}

	if got, err := pg.GetTokenByHash(ctx, "hash-of-sa-token"); err != nil || got == nil {
		t.Fatalf("GetTokenByHash = %v, %v", got, err)
	}

	if ok, err := pg.DeleteServiceAccount(ctx, sa.ID); err != nil || !ok {
		t.Fatalf("DeleteServiceAccount = %v, %v", ok, err)
	}

	if u, err := pg.GetUserByID(ctx, sa.ID); err != nil || u != nil {
		t.Fatalf("GetUserByID after delete = %v, %v; want nil", u, err)
	}

	if got, err := pg.GetTokenByHash(ctx, "hash-of-sa-token"); err != nil || got != nil {
		t.Fatalf("token %s after delete = %v, %v; want gone", token.ID, got, err)
	}

	if got, err := pg.GetOIDCPolicy(ctx, policy.ID); err != nil || got != nil {
		t.Fatalf("policy after delete = %v, %v; want gone", got, err)
	}

	var assignments, members int
	if err := pg.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_assignments WHERE principal_id = $1`, sa.ID).
		Scan(&assignments); err != nil {
		t.Fatal(err)
	}

	if err := pg.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_user_groups WHERE user_id = $1`, sa.ID).Scan(&members); err != nil {
		t.Fatal(err)
	}

	if assignments != 0 || members != 0 {
		t.Fatalf("left behind: %d assignments, %d group memberships", assignments, members)
	}

	// The other account keeps its assignment.
	if err := pg.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_assignments WHERE principal_id = $1`, keep.ID).
		Scan(&assignments); err != nil ||
		assignments != 1 {
		t.Fatalf("other account's assignments = %d, %v", assignments, err)
	}
}

func TestServiceAccounts_HoldProjectRoles(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	sa := newServiceAccount(t, pg, "ci-publisher")

	project := &models.Project{Name: "Acme"}
	if err := pg.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if err := pg.AddProjectMember(ctx, project.ID, sa.ID, "developer"); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	members, err := pg.ListProjectMembers(ctx, project.ID)
	if err != nil || len(members) != 1 || members[0].UserID != sa.ID || members[0].Role != "developer" {
		t.Fatalf("ListProjectMembers = %+v, %v", members, err)
	}

	grants, err := pg.ListProjectGrantsByUser(ctx, sa.ID)
	if err != nil || len(grants) != 1 || grants[0].ProjectID != project.ID || grants[0].Role != "developer" {
		t.Fatalf("ListProjectGrantsByUser = %+v, %v", grants, err)
	}
}

//nolint:gocyclo,cyclop // one scenario over a shared fixture; splitting would repeat the seeding
func TestOIDCPolicies_Store(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	sa := newServiceAccount(t, pg, "ci-publisher")
	frozen := newServiceAccount(t, pg, "ci-frozen")

	if _, err := pg.UpdateServiceAccount(ctx, frozen.ID, nil, new(true)); err != nil {
		t.Fatal(err)
	}

	const issuer = "https://token.actions.githubusercontent.com"

	newPolicy := func(name, saID string) *models.OIDCTrustPolicy {
		return &models.OIDCTrustPolicy{
			Name: name, Issuer: issuer, Audience: "https://oa.example", ServiceAccountID: saID, TokenTTLSeconds: 600, Enabled: true,
			Conditions: []models.OIDCCondition{
				{Claim: "repository", Patterns: []string{"acme/*", "other/widgets"}},
				{Claim: "ref", Patterns: []string{"refs/tags/v*"}},
			},
			CreatedBy: "admin",
		}
	}

	first, err := pg.CreateOIDCPolicy(ctx, newPolicy("first", sa.ID))
	if err != nil {
		t.Fatalf("CreateOIDCPolicy: %v", err)
	}

	if first.ID == "" || first.ServiceAccountName != "ci-publisher" || first.ServiceAccountDisabled || len(first.Conditions) != 2 ||
		first.Conditions[0].Claim != "repository" || len(first.Conditions[0].Patterns) != 2 || first.CreatedBy != "admin" {
		t.Fatalf("created policy = %+v", first)
	}

	if _, err := pg.CreateOIDCPolicy(ctx, newPolicy("first", sa.ID)); !IsUniqueViolation(err) {
		t.Fatalf("duplicate name: %v, want a unique violation", err)
	}

	if _, err := pg.CreateOIDCPolicy(ctx, newPolicy("ghost", "cccccccc-0000-4000-8000-000000000009")); !IsForeignKeyViolation(err) {
		t.Fatalf("unknown service account: %v, want a foreign key violation", err)
	}

	bad := newPolicy("short-ttl", sa.ID)
	bad.TokenTTLSeconds = 7200

	if _, err := pg.CreateOIDCPolicy(ctx, bad); err == nil {
		t.Fatal("a ttl above an hour was accepted by the database")
	}

	noConditions := newPolicy("open", sa.ID)
	noConditions.Conditions = []models.OIDCCondition{}

	if _, err := pg.CreateOIDCPolicy(ctx, noConditions); err == nil {
		t.Fatal("a policy without conditions was accepted by the database")
	}

	second, err := pg.CreateOIDCPolicy(ctx, newPolicy("second", frozen.ID))
	if err != nil || !second.ServiceAccountDisabled {
		t.Fatalf("policy of a disabled account = %+v, %v", second, err)
	}

	off := newPolicy("off", sa.ID)
	off.Enabled = false

	if _, err := pg.CreateOIDCPolicy(ctx, off); err != nil {
		t.Fatal(err)
	}

	other := newPolicy("elsewhere", sa.ID)
	other.Issuer = "https://gitlab.com"

	if _, err := pg.CreateOIDCPolicy(ctx, other); err != nil {
		t.Fatal(err)
	}

	all, err := pg.ListOIDCPolicies(ctx)
	if err != nil || len(all) != 4 {
		t.Fatalf("ListOIDCPolicies = %d policies, %v; want 4", len(all), err)
	}

	for i, name := range []string{"elsewhere", "first", "off", "second"} {
		if all[i].Name != name {
			t.Fatalf("ListOIDCPolicies[%d] = %s, want %s (ordered by name)", i, all[i].Name, name)
		}
	}

	enabled, err := pg.ListEnabledOIDCPoliciesByIssuer(ctx, issuer)
	if err != nil || len(enabled) != 2 || enabled[0].Name != "first" || enabled[1].Name != "second" {
		t.Fatalf("ListEnabledOIDCPoliciesByIssuer = %+v, %v; want first, second (oldest first, disabled excluded)", enabled, err)
	}

	first.Name, first.TokenTTLSeconds, first.Enabled = "renamed", 3600, false
	first.Conditions = []models.OIDCCondition{{Claim: "sub", Patterns: []string{"repo:acme/*"}}}

	updated, err := pg.UpdateOIDCPolicy(ctx, first)
	if err != nil || updated == nil || updated.Name != "renamed" || updated.TokenTTLSeconds != 3600 || updated.Enabled ||
		len(updated.Conditions) != 1 || updated.Conditions[0].Claim != "sub" {
		t.Fatalf("UpdateOIDCPolicy = %+v, %v", updated, err)
	}

	missing := *first
	missing.ID = "cccccccc-0000-4000-8000-000000000009"

	if got, err := pg.UpdateOIDCPolicy(ctx, &missing); err != nil || got != nil {
		t.Fatalf("update of an unknown policy = %v, %v; want nil", got, err)
	}

	if ok, err := pg.DeleteOIDCPolicy(ctx, first.ID); err != nil || !ok {
		t.Fatalf("DeleteOIDCPolicy = %v, %v", ok, err)
	}

	if ok, err := pg.DeleteOIDCPolicy(ctx, first.ID); err != nil || ok {
		t.Fatalf("second DeleteOIDCPolicy = %v, %v; want false", ok, err)
	}
}
