// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	projA = "11111111-1111-1111-1111-111111111111"
	projB = "22222222-2222-2222-2222-222222222222"
)

// projMockDB serves one user's project grants, targets and a fixed repository set.
type projMockDB struct {
	databasetest.BaseMockDB

	grants  []*models.ProjectGrant
	targets []*models.PermissionTarget
	repos   map[string]*models.Repository
}

func (m *projMockDB) ListProjectGrantsByUser(_ context.Context, _ string) ([]*models.ProjectGrant, error) {
	return m.grants, nil
}

func (m *projMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return m.targets, nil
}

func (m *projMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return m.repos[name], nil
}

func newProjDB(grants ...*models.ProjectGrant) *projMockDB {
	a, b, sys := projA, projB, models.SystemProjectID

	return &projMockDB{
		grants: grants,
		repos: map[string]*models.Repository{
			"in-a":       {Name: "in-a", ProjectID: &a},
			"in-b":       {Name: "in-b", ProjectID: &b},
			"in-sys":     {Name: "in-sys", ProjectID: &sys},
			"no-project": {Name: "no-project"},
		},
	}
}

func projUser() Principal {
	id := "user-1"

	return Principal{UserID: &id, Username: "u", Credential: CredentialSession}
}

func allowedActions(t *testing.T, e *Evaluator, p Principal, repo string) map[string]bool {
	t.Helper()

	got := map[string]bool{}

	for _, action := range []string{"read", "write", "delete", "manage", "annotate"} {
		ok, err := e.Evaluate(context.Background(), p, repo, "", action)
		if err != nil {
			t.Fatalf("Evaluate(%s, %s): %v", repo, action, err)
		}

		got[action] = ok
	}

	return got
}

func wantActions(actions ...string) map[string]bool {
	m := map[string]bool{"read": false, "write": false, "delete": false, "manage": false, "annotate": false}
	for _, a := range actions {
		m[a] = true
	}

	return m
}

func equalActions(a, b map[string]bool) bool {
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	for k, v := range b {
		if a[k] != v {
			return false
		}
	}

	return true
}

func g(project, role string) *models.ProjectGrant {
	return &models.ProjectGrant{ProjectID: project, Role: role}
}

func TestProjectAccessRoleActions(t *testing.T) {
	tests := []struct {
		role string
		want map[string]bool
	}{
		{models.ProjectRoleViewer, wantActions("read")},
		{models.ProjectRoleDeveloper, wantActions("read", "write")},
		{models.ProjectRoleMaintainer, wantActions("read", "write", "delete")},
		{models.ProjectRoleOwner, wantActions("read", "write", "delete")},
		{"admin", wantActions()},
		{"member", wantActions()},
		{"bogus", wantActions()},
		{"", wantActions()},
	}

	for _, tt := range tests {
		t.Run("role "+tt.role, func(t *testing.T) {
			e := NewEvaluator(newProjDB(g(projA, tt.role)), nil, false)

			if got := allowedActions(t, e, projUser(), "in-a"); !equalActions(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProjectAccessDeniedCases(t *testing.T) {
	scoped := projUser()
	scoped.TokenKind, scoped.TokenScopes = "access", []string{"some-target"}

	unscoped := projUser()
	unscoped.TokenKind = "access"

	deploy := projUser()
	deploy.TokenKind = TokenKindDeploy

	legacy := Principal{IsLegacy: true}

	owner := []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}

	tests := []struct {
		name      string
		grants    []*models.ProjectGrant
		principal Principal
		repo      string
		want      map[string]bool
	}{
		{"non-member", nil, projUser(), "in-a", wantActions()},
		{"other project does not leak", owner, projUser(), "in-b", wantActions()},
		{"system project grants nothing", []*models.ProjectGrant{g(models.SystemProjectID, models.ProjectRoleOwner)}, projUser(), "in-sys", wantActions()},
		{"repo without project", owner, projUser(), "no-project", wantActions()},
		{"unknown repo", owner, projUser(), "ghost", wantActions()},
		{"scoped token gets no project grant", owner, scoped, "in-a", wantActions()},
		{"anonymous gets no project grant", owner, Principal{IsAnonymous: true}, "in-a", wantActions()},
		{"unscoped token inherits", []*models.ProjectGrant{g(projA, models.ProjectRoleDeveloper)}, unscoped, "in-a", wantActions("read", "write")},
		{"deploy token inherits repo actions, never manage", owner, deploy, "in-a", wantActions("read", "write", "delete")},
		{"legacy key is unaffected by grants", nil, legacy, "in-a", wantActions("read", "write", "delete", "manage", "annotate")},
		{"project role never grants manage", owner, projUser(), "in-a", wantActions("read", "write", "delete")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEvaluator(newProjDB(tt.grants...), nil, false)

			if got := allowedActions(t, e, tt.principal, tt.repo); !equalActions(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestProjectAccessGroupGrants covers grants derived from several sources
// (recorded owner, membership, groups): the highest role per project wins and
// never leaks into another project.
func TestProjectAccessGroupGrants(t *testing.T) {
	tests := []struct {
		name   string
		grants []*models.ProjectGrant
		repo   string
		want   map[string]bool
	}{
		{
			"owner membership plus developer group",
			[]*models.ProjectGrant{g(projA, models.ProjectRoleDeveloper), g(projA, models.ProjectRoleOwner)},
			"in-a", wantActions("read", "write", "delete"),
		},
		{
			"viewer group plus maintainer group is maintainer",
			[]*models.ProjectGrant{g(projA, models.ProjectRoleViewer), g(projA, models.ProjectRoleMaintainer)},
			"in-a", wantActions("read", "write", "delete"),
		},
		{
			"order does not matter",
			[]*models.ProjectGrant{g(projA, models.ProjectRoleMaintainer), g(projA, models.ProjectRoleViewer)},
			"in-a", wantActions("read", "write", "delete"),
		},
		{
			"group grant in A does not leak to B",
			[]*models.ProjectGrant{g(projA, models.ProjectRoleMaintainer), g(projB, models.ProjectRoleViewer)},
			"in-b", wantActions("read"),
		},
		{
			"group grant in A only",
			[]*models.ProjectGrant{g(projA, models.ProjectRoleMaintainer)},
			"in-b", wantActions(),
		},
		{
			"unknown role ignored next to a known one",
			[]*models.ProjectGrant{g(projA, "superuser"), g(projA, models.ProjectRoleViewer)},
			"in-a", wantActions("read"),
		},
		{
			"unknown role alone grants nothing",
			[]*models.ProjectGrant{g(projA, "superuser")},
			"in-a", wantActions(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEvaluator(newProjDB(tt.grants...), nil, false)

			if got := allowedActions(t, e, projUser(), tt.repo); !equalActions(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProjectAccessUnionWithPermissionTargets(t *testing.T) {
	db := newProjDB(g(projA, models.ProjectRoleViewer))
	db.targets = []*models.PermissionTarget{{
		ID: "t1", Name: "deployers", RepositoryFilter: "in-.*", PathPattern: ".*", Actions: []string{"write", "annotate"},
	}}
	e := NewEvaluator(db, nil, false)

	// in-a: project read + target write/annotate; in-b, in-sys: target only.
	tests := []struct {
		repo string
		want map[string]bool
	}{
		{"in-a", wantActions("read", "write", "annotate")},
		{"in-b", wantActions("write", "annotate")},
		{"in-sys", wantActions("write", "annotate")},
	}

	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			if got := allowedActions(t, e, projUser(), tt.repo); !equalActions(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRepoReadFilterAppliesProjectGrants(t *testing.T) {
	scoped := projUser()
	scoped.TokenScopes = []string{"x"}

	tests := []struct {
		name      string
		grants    []*models.ProjectGrant
		principal Principal
		want      map[string]bool
	}{
		{"member sees own project only", []*models.ProjectGrant{g(projA, models.ProjectRoleViewer)}, projUser(),
			map[string]bool{"in-a": true, "in-b": false, "in-sys": false, "no-project": false}},
		{"group max role across projects", []*models.ProjectGrant{g(projA, models.ProjectRoleViewer), g(projB, models.ProjectRoleDeveloper)}, projUser(),
			map[string]bool{"in-a": true, "in-b": true, "in-sys": false}},
		{"unknown role shows nothing", []*models.ProjectGrant{g(projA, "bogus")}, projUser(),
			map[string]bool{"in-a": false}},
		{"system project membership shows nothing", []*models.ProjectGrant{g(models.SystemProjectID, models.ProjectRoleOwner)}, projUser(),
			map[string]bool{"in-a": false, "in-sys": false}},
		{"scoped token sees nothing from projects", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, scoped,
			map[string]bool{"in-a": false}},
		{"anonymous sees nothing from projects", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, Principal{IsAnonymous: true},
			map[string]bool{"in-a": false}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjDB(tt.grants...)
			e := NewEvaluator(db, nil, false)

			filter, err := e.RepoReadFilter(context.Background(), tt.principal)
			if err != nil {
				t.Fatal(err)
			}

			for name, want := range tt.want {
				got, err := filter(db.repos[name])
				if err != nil {
					t.Fatal(err)
				}

				if got != want {
					t.Errorf("%s: got %v, want %v", name, got, want)
				}
			}
		})
	}
}

func TestProjectRoleAllows(t *testing.T) {
	actions := []string{"read", "write", "delete", "manage"}
	want := map[string][]bool{
		models.ProjectRoleViewer:     {true, false, false, false},
		models.ProjectRoleDeveloper:  {true, true, false, false},
		models.ProjectRoleMaintainer: {true, true, true, false},
		models.ProjectRoleOwner:      {true, true, true, false},
		"bogus":                      {false, false, false, false},
	}

	for role, exp := range want {
		for i, action := range actions {
			if got := projectRoleAllows(role, action); got != exp[i] {
				t.Errorf("projectRoleAllows(%q, %q) = %v, want %v", role, action, got, exp[i])
			}
		}
	}
}

func TestMaxProjectRole(t *testing.T) {
	tests := []struct{ a, b, want string }{
		{"", models.ProjectRoleViewer, models.ProjectRoleViewer},
		{models.ProjectRoleViewer, models.ProjectRoleDeveloper, models.ProjectRoleDeveloper},
		{models.ProjectRoleMaintainer, models.ProjectRoleViewer, models.ProjectRoleMaintainer},
		{models.ProjectRoleOwner, models.ProjectRoleMaintainer, models.ProjectRoleOwner},
		{models.ProjectRoleMaintainer, models.ProjectRoleOwner, models.ProjectRoleOwner},
		{models.ProjectRoleViewer, "bogus", models.ProjectRoleViewer},
		{"bogus", models.ProjectRoleViewer, models.ProjectRoleViewer},
		{models.ProjectRoleDeveloper, models.ProjectRoleDeveloper, models.ProjectRoleDeveloper},
	}

	for _, tt := range tests {
		if got := maxProjectRole(tt.a, tt.b); got != tt.want {
			t.Errorf("maxProjectRole(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func (m *projMockDB) ListPublicRepositoryNames(context.Context) ([]string, error) {
	return nil, nil
}
