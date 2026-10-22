// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

type tokenFixture struct {
	db      *database.PostgresDB
	project *models.Project
	owner   *models.User
	dev     *models.User
}

func newTokenFixture(t *testing.T) tokenFixture {
	t.Helper()

	ctx := context.Background()
	db := freshPostgres(t, 5)

	mk := func(name string) *models.User {
		u, err := db.CreateUser(ctx, &models.User{Username: name})
		if err != nil {
			t.Fatal(err)
		}

		return u
	}

	f := tokenFixture{db: db, owner: mk("tk-owner"), dev: mk("tk-dev")}
	f.project = &models.Project{Name: "tk-acme", OwnerID: &f.owner.ID}

	if err := db.CreateProject(ctx, f.project); err != nil {
		t.Fatal(err)
	}

	if err := db.AddProjectMember(ctx, f.project.ID, f.dev.ID, models.ProjectRoleDeveloper); err != nil {
		t.Fatal(err)
	}

	return f
}

func (f tokenFixture) mint(t *testing.T, createdBy, name string, maxActive int) (string, *models.Token, error) {
	t.Helper()

	return auth.CreateProjectToken(context.Background(), f.db, database.ProjectTokenSpec{
		ProjectID: f.project.ID, ProjectSlug: f.project.Slug, Name: name, Role: models.ProjectRoleDeveloper,
		CreatedBy: createdBy, MaxActive: maxActive,
	})
}

// alive reports whether the raw token still authenticates: not revoked and its
// service user not disabled.
func (f tokenFixture) alive(t *testing.T, secret string) bool {
	t.Helper()

	tok, user, err := auth.ValidateToken(context.Background(), f.db, secret)
	if err != nil {
		return false
	}

	return tok != nil && user != nil && !user.Disabled
}

func TestProjectTokenLifecycle(t *testing.T) {
	f := newTokenFixture(t)
	ctx := context.Background()

	secret, tok, err := f.mint(t, f.owner.ID, "ci", 0)
	if err != nil {
		t.Fatal(err)
	}

	if !f.alive(t, secret) || tok.ProjectID == nil || *tok.ProjectID != f.project.ID {
		t.Fatal("fresh project token is not usable")
	}

	svc, err := f.db.GetUserByID(ctx, tok.UserID)
	if err != nil || svc == nil || !svc.IsService() || svc.ProjectID == nil || *svc.ProjectID != f.project.ID {
		t.Fatalf("service user = %+v, %v", svc, err)
	}

	if grants, err := f.db.ListProjectGrantsByUser(ctx, svc.ID); err != nil || len(grants) != 1 || grants[0].Role != models.ProjectRoleDeveloper {
		t.Fatalf("service grants = %v, %v", grants, err)
	}

	assertServiceUserHidden(t, f, svc)

	// Revoke is immediate, auth cache included.
	if ok, err := f.db.RevokeProjectToken(ctx, f.project.ID, tok.ID); err != nil || !ok {
		t.Fatalf("revoke = %v, %v", ok, err)
	}

	if f.alive(t, secret) {
		t.Fatal("revoked token still authenticates")
	}

	if ok, _ := f.db.RevokeProjectToken(ctx, f.project.ID, tok.ID); ok {
		t.Fatal("second revoke reported success")
	}
}

func assertServiceUserHidden(t *testing.T, f tokenFixture, svc *models.User) {
	t.Helper()

	ctx := context.Background()

	users, _, err := f.db.ListUsers(ctx, 1, 1000)
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range users {
		if u.IsService() {
			t.Fatalf("ListUsers returned service user %s", u.Username)
		}
	}

	members, err := f.db.ListProjectMembers(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range members {
		if m.UserID == svc.ID {
			t.Fatal("ListProjectMembers returned the service user")
		}
	}
}

func TestProjectTokenLimitsAndNames(t *testing.T) {
	f := newTokenFixture(t)

	if _, _, err := f.mint(t, f.owner.ID, "a", 2); err != nil {
		t.Fatal(err)
	}

	if _, _, err := f.mint(t, f.owner.ID, "a", 2); !errors.Is(err, database.ErrProjectTokenName) {
		t.Fatalf("duplicate name err = %v", err)
	}

	if _, tok, err := f.mint(t, f.owner.ID, "b", 2); err != nil {
		t.Fatal(err)
	} else if _, _, err := f.mint(t, f.owner.ID, "c", 2); !errors.Is(err, database.ErrProjectTokenLimit) {
		t.Fatalf("limit err = %v", err)
	} else if ok, err := f.db.RevokeProjectToken(context.Background(), f.project.ID, tok.ID); err != nil || !ok {
		t.Fatal("revoke")
	}

	// Revoking frees both the slot and the name.
	if _, _, err := f.mint(t, f.owner.ID, "b", 2); err != nil {
		t.Fatalf("reuse of a revoked token's name: %v", err)
	}
}

// TestProjectTokenCreatorLeaves: decision 5, the creator's tokens die with
// their membership, in every way a creator can go.
func TestProjectTokenCreatorLeaves(t *testing.T) {
	ctx := context.Background()

	remove := func(t *testing.T, f tokenFixture) {
		t.Helper()

		if err := f.db.ChangeProjectMember(ctx, f.project.ID, f.dev.ID, "", nil); err != nil {
			t.Fatal(err)
		}
	}

	disable := func(t *testing.T, f tokenFixture) {
		t.Helper()

		f.dev.Disabled = true
		if err := f.db.UpdateUser(ctx, f.dev); err != nil {
			t.Fatal(err)
		}
	}

	hardDelete := func(t *testing.T, f tokenFixture) {
		t.Helper()

		if _, err := f.db.GetDB().ExecContext(ctx, `DELETE FROM auth_users WHERE id = $1`, f.dev.ID); err != nil {
			t.Fatal(err)
		}
	}

	for name, leave := range map[string]func(*testing.T, tokenFixture){"removed": remove, "disabled": disable, "deleted": hardDelete} {
		t.Run(name, func(t *testing.T) {
			f := newTokenFixture(t)

			devSecret, _, err := f.mint(t, f.dev.ID, "dev-ci", 0)
			if err != nil {
				t.Fatal(err)
			}

			ownerSecret, _, err := f.mint(t, f.owner.ID, "owner-ci", 0)
			if err != nil {
				t.Fatal(err)
			}

			leave(t, f)

			if f.alive(t, devSecret) {
				t.Fatal("the leaving creator's token survived")
			}

			if !f.alive(t, ownerSecret) {
				t.Fatal("another creator's token was revoked")
			}
		})
	}
}

func TestProjectTokenAdminList(t *testing.T) {
	f := newTokenFixture(t)

	if _, _, err := f.mint(t, f.dev.ID, "listed", 0); err != nil {
		t.Fatal(err)
	}

	all, _, err := f.db.ListAllTokens(context.Background(), 1, 1000)
	if err != nil {
		t.Fatal(err)
	}

	for _, tok := range all {
		if tok.Name == "listed" {
			if tok.ProjectName != f.project.Name || tok.CreatedByName != f.dev.Username || deref(tok.ProjectRole) != models.ProjectRoleDeveloper {
				t.Fatalf("admin row = %+v", tok)
			}

			return
		}
	}

	t.Fatal("project token missing from the admin list")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
