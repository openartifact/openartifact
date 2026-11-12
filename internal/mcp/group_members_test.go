// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"reflect"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// groupStack adds the npm group npm-all to the stack: web-npm (priority 0) and
// hidden (priority 1) as members, listed in reverse to show the order is the
// priority. The read-only user may read the group and web-npm, not hidden.
func groupStack(t *testing.T) *stack {
	t.Helper()

	st := newStack(t, nil)
	st.db.repos = append(st.db.repos, &models.Repository{ID: "r-group", Name: "npm-all", Format: "npm", RepoType: "group"})
	st.db.members = map[string][]*models.RepositoryGroupMember{"r-group": {
		{GroupRepoID: "r-group", MemberRepoID: "r-npm", Priority: 0},
		{GroupRepoID: "r-group", MemberRepoID: "r-secret", Priority: 1},
	}}
	st.db.targets["reader"][0].RepositoryFilter = "^(web-npm|images|npm-all)$"

	return st
}

func members(t *testing.T, resp toolResponse) any {
	t.Helper()

	if resp.Result.IsError {
		t.Fatalf("tool error: %s", toolText(t, resp))
	}

	return resp.Result.StructuredContent["members"]
}

// TestGetRepositoryListsGroupMembers: get_repository names the members of a group in
// serving order, so a client can ask check_availability of the member that serves a
// version; a member the caller cannot read is left out, as it is everywhere else.
func TestGetRepositoryListsGroupMembers(t *testing.T) {
	st := groupStack(t)

	webNPM := map[string]any{"name": "web-npm", "format": "npm", "repo_type": "proxy", "priority": float64(0)}
	hidden := map[string]any{"name": "hidden", "format": "npm", "repo_type": "hosted", "priority": float64(1)}

	for _, tc := range []struct {
		user string
		want any
	}{
		{"admin", []any{webNPM, hidden}},
		{"reader", []any{webNPM}},
	} {
		if got := members(t, st.call(tc.user, "get_repository", map[string]any{"name": "npm-all"})); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: members = %v, want %v", tc.user, got, tc.want)
		}
	}
}

// TestGetRepositoryGroupMembersAreTheServedOnes: like serving, members leaves out a
// blocked member, a nested group and a member of another format, even for an admin
// who can read them all.
func TestGetRepositoryGroupMembersAreTheServedOnes(t *testing.T) {
	st := groupStack(t)
	st.db.repos = append(st.db.repos,
		&models.Repository{ID: "r-blocked", Name: "npm-blocked", Format: "npm", RepoType: "proxy", Blocked: true},
		&models.Repository{ID: "r-nested", Name: "npm-nested", Format: "npm", RepoType: "group"},
		&models.Repository{ID: "r-pypi", Name: "py-hosted", Format: "pypi", RepoType: "hosted"},
	)
	st.db.members["r-group"] = []*models.RepositoryGroupMember{
		{GroupRepoID: "r-group", MemberRepoID: "r-blocked", Priority: 0},
		{GroupRepoID: "r-group", MemberRepoID: "r-nested", Priority: 1},
		{GroupRepoID: "r-group", MemberRepoID: "r-pypi", Priority: 2},
		{GroupRepoID: "r-group", MemberRepoID: "r-npm", Priority: 3},
	}

	want := []any{map[string]any{"name": "web-npm", "format": "npm", "repo_type": "proxy", "priority": float64(3)}}
	if got := members(t, st.call("admin", "get_repository", map[string]any{"name": "npm-all"})); !reflect.DeepEqual(got, want) {
		t.Errorf("members = %v, want only web-npm", got)
	}
}

// TestGetRepositoryMembersOnlyForGroups: a proxy or hosted repository has no members
// field, and a group whose members the caller cannot read has an empty list rather
// than none, so "no members visible" is not mistaken for "not a group".
func TestGetRepositoryMembersOnlyForGroups(t *testing.T) {
	st := groupStack(t)

	if got := members(t, st.call("reader", "get_repository", map[string]any{"name": "web-npm"})); got != nil {
		t.Errorf("proxy repository: members = %v, want none", got)
	}

	st.db.members["r-group"] = st.db.members["r-group"][1:] // only hidden is left

	if got := members(t, st.call("reader", "get_repository", map[string]any{"name": "npm-all"})); !reflect.DeepEqual(got, []any{}) {
		t.Errorf("group with no readable member: members = %v, want []", got)
	}
}
