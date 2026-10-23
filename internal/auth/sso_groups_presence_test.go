// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func groupsPresenceFixture() (*provMockDB, *models.SSOProvider) {
	db := newProvMockDB()
	extID := "sub|presence"
	slug := "okta"
	db.addUser(&models.User{ID: "user-presence", Username: "presence", ExternalID: &extID, ProviderSlug: &slug})
	db.addGroup(&models.Group{ID: "grp-a-id", Name: "local-a"})

	return db, testProvider(true, false, map[string]string{"a": "local-a"})
}

// An absent groups claim (misconfigured IdP, Entra overage) must leave the
// user's SSO-managed memberships untouched: no sync call at all.
func TestProvisioner_GroupsAbsent_KeepsMemberships(t *testing.T) {
	db, provider := groupsPresenceFixture()
	claims := testClaims("sub|presence", "p@example.com", "presence", nil)
	claims.GroupsPresent = false

	if _, err := newTestProvisioner(db).Provision(context.Background(), provider, claims); err != nil {
		t.Fatalf("provision: %v", err)
	}

	if len(db.syncGroupsArgs) != 0 {
		t.Fatalf("absent groups claim must not sync memberships, got %v", db.syncGroupsArgs)
	}
}

// A present but empty groups claim is the IdP saying the user has no groups:
// memberships are synced to the empty set.
func TestProvisioner_GroupsPresentEmpty_ClearsMemberships(t *testing.T) {
	db, provider := groupsPresenceFixture()
	claims := testClaims("sub|presence", "p@example.com", "presence", []string{})

	if _, err := newTestProvisioner(db).Provision(context.Background(), provider, claims); err != nil {
		t.Fatalf("provision: %v", err)
	}

	if len(db.syncGroupsArgs) != 1 {
		t.Fatalf("expected 1 sync call, got %d", len(db.syncGroupsArgs))
	}

	if got := db.syncGroupsArgs[0].groupIDs; len(got) != 0 {
		t.Fatalf("empty claim must sync to the empty set, got %v", got)
	}
}
