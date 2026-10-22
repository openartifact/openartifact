// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

var errProjectDB = errors.New("db down")

// actionsMockDB adds project lookup by name to projMockDB.
type actionsMockDB struct {
	projMockDB

	projects  map[string]*models.Project
	grantsErr error
}

func (m *actionsMockDB) GetProjectByName(_ context.Context, name string) (*models.Project, error) {
	return m.projects[name], nil
}

func (m *actionsMockDB) ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error) {
	if m.grantsErr != nil {
		return nil, m.grantsErr
	}

	return m.projMockDB.ListProjectGrantsByUser(ctx, userID)
}

func newActionsDB(grants ...*models.ProjectGrant) *actionsMockDB {
	return &actionsMockDB{
		projMockDB: *newProjDB(grants...),
		projects: map[string]*models.Project{
			"a":      {ID: projA, Name: "a"},
			"b":      {ID: projB, Name: "b"},
			"system": {ID: models.SystemProjectID, Name: "system"},
		},
	}
}

func adminUser() Principal {
	p := projUser()
	p.Username = "root"

	return p
}

func withManageTarget(db *actionsMockDB) *actionsMockDB {
	db.targets = []*models.PermissionTarget{{
		ID: "admin", Name: "admin", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"manage"},
	}}

	return db
}

func allProjectActions() []ProjectAction {
	return []ProjectAction{
		ProjectActionRead, ProjectActionMembersManage, ProjectActionUpdate, ProjectActionDelete,
		ProjectActionRepoCreate, ProjectActionRepoAdmin, ProjectActionRepoTransfer,
	}
}

func TestProjectActionMatrix(t *testing.T) {
	want := map[string][]bool{
		// read, members.manage, update, delete, repo.create, repo.admin, repo.transfer
		"":                           {false, false, false, false, false, false, false},
		"bogus":                      {false, false, false, false, false, false, false},
		models.ProjectRoleViewer:     {true, false, false, false, false, false, false},
		models.ProjectRoleDeveloper:  {true, false, false, false, false, false, false},
		models.ProjectRoleMaintainer: {true, true, false, false, true, true, false},
		models.ProjectRoleOwner:      {true, true, true, true, true, true, true},
	}

	for role, exp := range want {
		for i, action := range allProjectActions() {
			if got := ProjectRoleAllows(role, false, action); got != exp[i] {
				t.Errorf("ProjectRoleAllows(%q, false, %q) = %v, want %v", role, action, got, exp[i])
			}
		}
	}

	for _, action := range allProjectActions() {
		if !ProjectRoleAllows("", true, action) {
			t.Errorf("admin must be allowed %q without a role", action)
		}
	}

	if ProjectRoleAllows(models.ProjectRoleOwner, true, "project.explode") {
		t.Error("unknown action must never be allowed")
	}
}

func TestProjectDecisionFor(t *testing.T) {
	tests := []struct {
		name   string
		role   string
		admin  bool
		action ProjectAction
		want   ProjectDecision
	}{
		{"no role is hidden", "", false, ProjectActionRead, ProjectHidden},
		{"unknown role is hidden", "bogus", false, ProjectActionRead, ProjectHidden},
		{"viewer reads", models.ProjectRoleViewer, false, ProjectActionRead, ProjectAllow},
		{"viewer cannot manage members", models.ProjectRoleViewer, false, ProjectActionMembersManage, ProjectForbidden},
		{"maintainer cannot update", models.ProjectRoleMaintainer, false, ProjectActionUpdate, ProjectForbidden},
		{"owner deletes", models.ProjectRoleOwner, false, ProjectActionDelete, ProjectAllow},
		{"admin without role", "", true, ProjectActionDelete, ProjectAllow},
		{"admin unknown action", "", true, "nope", ProjectHidden},
		{"role with unknown action is forbidden", models.ProjectRoleOwner, false, "nope", ProjectForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectDecisionFor(tt.role, tt.admin, tt.action); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecideProject(t *testing.T) {
	scoped := projUser()
	scoped.TokenScopes = []string{"t"}

	deploy := projUser()
	deploy.TokenKind = TokenKindDeploy

	owner := g(projA, models.ProjectRoleOwner)

	tests := []struct {
		name      string
		grants    []*models.ProjectGrant
		admin     bool
		principal Principal
		project   string
		action    ProjectAction
		want      ProjectDecision
	}{
		{"owner deletes", []*models.ProjectGrant{owner}, false, projUser(), "a", ProjectActionDelete, ProjectAllow},
		{"viewer cannot update", []*models.ProjectGrant{g(projA, models.ProjectRoleViewer)}, false, projUser(), "a", ProjectActionUpdate, ProjectForbidden},
		{"non-member hidden", nil, false, projUser(), "a", ProjectActionRead, ProjectHidden},
		{"other project hidden", []*models.ProjectGrant{owner}, false, projUser(), "b", ProjectActionRead, ProjectHidden},
		{"unknown project hidden", []*models.ProjectGrant{owner}, false, projUser(), "ghost", ProjectActionRead, ProjectHidden},
		{"group grant", []*models.ProjectGrant{g(projA, models.ProjectRoleViewer), g(projA, models.ProjectRoleMaintainer)}, false, projUser(), "a", ProjectActionMembersManage, ProjectAllow},
		{"system project grants nothing", []*models.ProjectGrant{g(models.SystemProjectID, models.ProjectRoleOwner)}, false, projUser(), "system", ProjectActionRead, ProjectHidden},
		{"admin on unknown project", nil, true, adminUser(), "ghost", ProjectActionDelete, ProjectAllow},
		{"admin on system project", nil, true, adminUser(), "system", ProjectActionRead, ProjectAllow},
		{"scoped token hidden", []*models.ProjectGrant{owner}, false, scoped, "a", ProjectActionRead, ProjectHidden},
		{"deploy token hidden", []*models.ProjectGrant{owner}, true, deploy, "a", ProjectActionRead, ProjectHidden},
		{"anonymous hidden", []*models.ProjectGrant{owner}, true, Principal{IsAnonymous: true}, "a", ProjectActionRead, ProjectHidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newActionsDB(tt.grants...)
			if tt.admin {
				withManageTarget(db)
			}

			e := NewEvaluator(db, nil, false)

			got, err := e.DecideProject(context.Background(), tt.principal, tt.project, tt.action)
			if err != nil {
				t.Fatal(err)
			}

			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}

			allowed, err := e.EvaluateProject(context.Background(), tt.principal, tt.project, tt.action)
			if err != nil || allowed != (tt.want == ProjectAllow) {
				t.Fatalf("EvaluateProject = %v, %v; want %v", allowed, err, tt.want == ProjectAllow)
			}
		})
	}
}

func roleGrants() []*models.ProjectGrant {
	return []*models.ProjectGrant{
		g(projA, models.ProjectRoleViewer), g(projA, models.ProjectRoleDeveloper),
		g(projB, models.ProjectRoleOwner), g(models.SystemProjectID, models.ProjectRoleOwner),
	}
}

func TestProjectRoles_Member(t *testing.T) {
	ctx := context.Background()

	t.Run("member roles take the highest and drop the system project", func(t *testing.T) {
		e := NewEvaluator(newActionsDB(roleGrants()...), nil, false)

		roles, err := e.ProjectRoles(ctx, projUser())
		if err != nil {
			t.Fatal(err)
		}

		if len(roles) != 2 || roles[projA] != models.ProjectRoleDeveloper || roles[projB] != models.ProjectRoleOwner {
			t.Fatalf("roles = %v", roles)
		}

		role, admin, err := e.ProjectRole(ctx, projUser(), projA)
		if err != nil || role != models.ProjectRoleDeveloper || admin {
			t.Fatalf("ProjectRole = %q, %v, %v", role, admin, err)
		}

		role, _, err = e.ProjectRole(ctx, projUser(), models.SystemProjectID)
		if err != nil || role != "" {
			t.Fatalf("system project role = %q, %v", role, err)
		}
	})

	t.Run("admin is flagged but has no implicit role", func(t *testing.T) {
		e := NewEvaluator(withManageTarget(newActionsDB()), nil, false)

		role, admin, err := e.ProjectRole(ctx, adminUser(), projA)
		if err != nil || role != "" || !admin {
			t.Fatalf("ProjectRole = %q, %v, %v", role, admin, err)
		}
	})
}

func TestProjectRoles_NoRoleForBlockedPrincipals(t *testing.T) {
	ctx := context.Background()

	scoped := projUser()
	scoped.TokenScopes = []string{"t"}

	deploy := projUser()
	deploy.TokenKind = TokenKindDeploy

	for name, p := range map[string]Principal{
		"scoped token": scoped, "deploy token": deploy, "anonymous": {IsAnonymous: true},
	} {
		t.Run(name+" yields nothing", func(t *testing.T) {
			e := NewEvaluator(newActionsDB(roleGrants()...), nil, false)

			roles, err := e.ProjectRoles(ctx, p)
			if err != nil || len(roles) != 0 {
				t.Fatalf("ProjectRoles = %v, %v", roles, err)
			}

			role, admin, err := e.ProjectRole(ctx, p, projA)
			if err != nil || role != "" || admin {
				t.Fatalf("ProjectRole = %q, %v, %v", role, admin, err)
			}
		})
	}
}

func TestProjectRoles_StoreErrorPropagates(t *testing.T) {
	ctx := context.Background()
	db := newActionsDB(roleGrants()...)
	db.grantsErr = errProjectDB
	e := NewEvaluator(db, nil, false)

	if _, err := e.ProjectRoles(ctx, projUser()); !errors.Is(err, errProjectDB) {
		t.Fatalf("ProjectRoles err = %v", err)
	}

	if _, _, err := e.ProjectRole(ctx, projUser(), projA); !errors.Is(err, errProjectDB) {
		t.Fatalf("ProjectRole err = %v", err)
	}

	if _, err := e.DecideProject(ctx, projUser(), "a", ProjectActionRead); !errors.Is(err, errProjectDB) {
		t.Fatalf("DecideProject err = %v", err)
	}
}

func TestEvaluateRepoAdmin(t *testing.T) {
	scoped := projUser()
	scoped.TokenScopes = []string{"t"}

	deploy := projUser()
	deploy.TokenKind = TokenKindDeploy

	scopedManage := &models.PermissionTarget{
		ID: "scoped", Name: "scoped", RepositoryFilter: "^in-a$", PathPattern: ".*", Actions: []string{"read", "manage"},
	}

	tests := []struct {
		name      string
		grants    []*models.ProjectGrant
		targets   []*models.PermissionTarget
		principal Principal
		repo      string
		want      bool
	}{
		{"maintainer of the repo's project", []*models.ProjectGrant{g(projA, models.ProjectRoleMaintainer)}, nil, projUser(), "in-a", true},
		{"owner of the repo's project", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, nil, projUser(), "in-a", true},
		{"developer is not enough", []*models.ProjectGrant{g(projA, models.ProjectRoleDeveloper)}, nil, projUser(), "in-a", false},
		{"maintainer of another project", []*models.ProjectGrant{g(projB, models.ProjectRoleMaintainer)}, nil, projUser(), "in-a", false},
		{"system project repo is never delegated", []*models.ProjectGrant{g(models.SystemProjectID, models.ProjectRoleOwner)}, nil, projUser(), "in-sys", false},
		{"repo without a project", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, nil, projUser(), "no-project", false},
		{"unknown repo", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, nil, projUser(), "ghost", false},
		{"global admin", nil, []*models.PermissionTarget{{ID: "m", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"manage"}}}, adminUser(), "in-a", true},
		{"repo-scoped manage target does not qualify", nil, []*models.PermissionTarget{scopedManage}, projUser(), "in-a", false},
		{"scoped token", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, nil, scoped, "in-a", false},
		{"deploy token", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, nil, deploy, "in-a", false},
		{"anonymous", []*models.ProjectGrant{g(projA, models.ProjectRoleOwner)}, nil, Principal{IsAnonymous: true}, "in-a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newActionsDB(tt.grants...)
			db.targets = tt.targets

			got, err := NewEvaluator(db, nil, false).EvaluateRepoAdmin(context.Background(), tt.principal, tt.repo)
			if err != nil || got != tt.want {
				t.Fatalf("EvaluateRepoAdmin = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// A project role is never a global manage right, and a repo-scoped manage
// target keeps exactly the rights it had.
func TestProjectMaintainerIsNotManage(t *testing.T) {
	db := newActionsDB(g(projA, models.ProjectRoleOwner))
	e := NewEvaluator(db, nil, false)

	for _, repo := range []string{"", "in-a"} {
		ok, err := e.Evaluate(context.Background(), projUser(), repo, "", "manage")
		if err != nil || ok {
			t.Fatalf("Evaluate(manage, %q) for a project owner = %v, %v; want false", repo, ok, err)
		}
	}

	db.targets = []*models.PermissionTarget{{ID: "s", RepositoryFilter: "^in-a$", PathPattern: ".*", Actions: []string{"manage"}}}
	db.grants = nil

	if ok, err := e.Evaluate(context.Background(), projUser(), "in-a", "", "manage"); err != nil || !ok {
		t.Fatalf("repo-scoped manage target lost its own right: %v, %v", ok, err)
	}
}

func TestEvaluateRepoAdminStoreError(t *testing.T) {
	db := newActionsDB(g(projA, models.ProjectRoleOwner))
	db.grantsErr = errProjectDB

	if _, err := NewEvaluator(db, nil, false).EvaluateRepoAdmin(context.Background(), projUser(), "in-a"); !errors.Is(err, errProjectDB) {
		t.Fatalf("err = %v", err)
	}
}
