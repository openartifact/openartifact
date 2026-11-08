// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// seedAdmins creates n human users, each holding admin-all directly, and
// returns their ids and the admin-all target id.
func seedAdmins(t *testing.T, pg *PostgresDB, n int) ([]string, string) {
	t.Helper()

	ctx := context.Background()

	target, err := pg.GetPermissionTargetByName(ctx, "admin-all")
	if err != nil || target == nil {
		t.Fatalf("admin-all target: %v %v", target, err)
	}

	hash := "x"
	ids := make([]string, 0, n)

	for _, name := range []string{"admin-a", "admin-b", "admin-c"}[:n] {
		email := name + "@example.test"

		u, err := pg.CreateUser(ctx, &models.User{Username: name, Email: &email, PasswordHash: &hash})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}

		if err := pg.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
			PermissionTargetID: target.ID, PrincipalType: "user", PrincipalID: u.ID,
		}); err != nil {
			t.Fatalf("assign admin-all: %v", err)
		}

		ids = append(ids, u.ID)
	}

	return ids, target.ID
}

func TestLastAdminSurvivesDisableAndUnassign(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	disabled := true

	ids, target := seedAdmins(t, pg, 2)

	// With two admins one may be disabled; the other is then the last.
	if _, err := pg.PatchUser(ctx, ids[0], UserPatch{Disabled: &disabled}); err != nil {
		t.Fatalf("disable first admin: %v", err)
	}

	if _, err := pg.PatchUser(ctx, ids[1], UserPatch{Disabled: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disable last admin = %v, want ErrLastAdmin", err)
	}

	if err := pg.DeletePermissionAssignment(ctx, target, "user", ids[1]); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("unassign last admin = %v, want ErrLastAdmin", err)
	}

	got, err := pg.GetUserByID(ctx, ids[1])
	if err != nil || got == nil || got.Disabled {
		t.Fatalf("last admin was disabled: %+v %v", got, err)
	}

	// Removing the grant of an already-disabled admin is fine.
	if err := pg.DeletePermissionAssignment(ctx, target, "user", ids[0]); err != nil {
		t.Fatalf("unassign disabled admin: %v", err)
	}
}

func TestConcurrentDisableKeepsOneAdmin(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	disabled := true

	ids, _ := seedAdmins(t, pg, 2)
	errs := make(chan error, len(ids))

	for _, id := range ids {
		go func() {
			_, err := pg.PatchUser(ctx, id, UserPatch{Disabled: &disabled})
			errs <- err
		}()
	}

	var refused int

	for range ids {
		if err := <-errs; errors.Is(err, ErrLastAdmin) {
			refused++
		} else if err != nil {
			t.Fatalf("PatchUser: %v", err)
		}
	}

	if refused != 1 {
		t.Fatalf("refused = %d, want exactly 1 of two concurrent disables", refused)
	}
}
