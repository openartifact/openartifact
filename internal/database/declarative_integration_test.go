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

func TestDeclarativeObjectsRoundTrip(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()

	items := []models.DeclarativeObject{
		{Kind: "User", Name: "bob", ObjectID: "u1", AppliedHash: "h1"},
		{Kind: "Repository", Name: "maven/a", ObjectID: "r1"},
	}

	stored, err := pg.UpsertDeclarativeObjects(ctx, items)
	if err != nil || len(stored) != 2 || stored[0].AppliedAt.IsZero() {
		t.Fatalf("upsert = %+v, %v", stored, err)
	}

	// Upserting the same keys again is idempotent and replaces the values.
	items[0].ObjectID = "u2"
	if _, err := pg.UpsertDeclarativeObjects(ctx, items); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := pg.ListDeclarativeObjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(got) != 2 || got[0].Kind != "Repository" || got[1].Name != "bob" || got[1].ObjectID != "u2" || got[1].AppliedHash != "h1" {
		t.Fatalf("list = %+v", got)
	}

	// One invalid item rolls back the whole batch.
	bad := []models.DeclarativeObject{{Kind: "User", Name: "carol", ObjectID: "u3"}, {Kind: "User", Name: "dave"}}
	if _, err := pg.UpsertDeclarativeObjects(ctx, bad); err == nil {
		t.Fatal("empty object_id must violate the CHECK constraint")
	}

	if got, _ := pg.ListDeclarativeObjects(ctx); len(got) != 2 {
		t.Fatalf("failed batch leaked rows: %+v", got)
	}

	for _, want := range []bool{true, false} {
		deleted, err := pg.DeleteDeclarativeObject(ctx, "Repository", "maven/a")
		if err != nil || deleted != want {
			t.Fatalf("delete = %v, %v; want %v", deleted, err, want)
		}
	}
}

func TestListPermissionAssignmentsForTarget(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	seed := seedACL(t, pg, newQuotaSeeder(t, pg, "decl-assign", "raw"))

	got, err := pg.ListPermissionAssignmentsForTarget(context.Background(), seed.target.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(got) != 2 || got[0].PrincipalType != "group" || got[0].PrincipalName != "listers" ||
		got[1].PrincipalType != "user" || got[1].PrincipalName != "lister" || got[1].PrincipalID != seed.user.ID {
		t.Fatalf("assignments = %+v", got)
	}
}
