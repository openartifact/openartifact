// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// projectAuthzFixture runs against real Postgres with a cache TTL (a minute)
// far longer than any test, so only the invalidation hooks can make a
// membership change visible.
type projectAuthzFixture struct {
	pg      *database.PostgresDB
	eval    *auth.Evaluator
	owner   *models.User
	alice   *models.User
	project *models.Project
	repo    *models.Repository
}

func newProjectAuthzFixture(t *testing.T) *projectAuthzFixture {
	t.Helper()

	pg := freshPostgresCfg(t, 5, func(c *database.DatabaseConfig) { c.AuthCacheTTLMillis = 60_000 })
	ctx := context.Background()

	owner, err := pg.CreateUser(ctx, &models.User{Username: "proj-owner"})
	if err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}

	alice, err := pg.CreateUser(ctx, &models.User{Username: "proj-alice"})
	if err != nil {
		t.Fatalf("CreateUser alice: %v", err)
	}

	project := &models.Project{Name: "acme", OwnerID: &owner.ID}
	if err := pg.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	repo := &models.Repository{Name: "acme-raw", Format: "raw", RepoType: "hosted", ProjectID: &project.ID}
	if err := pg.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	return &projectAuthzFixture{pg: pg, eval: auth.NewEvaluator(pg, nil, false), owner: owner, alice: alice, project: project, repo: repo}
}

func (f *projectAuthzFixture) principal(u *models.User) auth.Principal {
	id := u.ID

	return auth.Principal{UserID: &id, Username: u.Username, Credential: auth.CredentialSession}
}

// actions returns the subset of read/write/delete/manage u holds on repoName.
func (f *projectAuthzFixture) actions(t *testing.T, u *models.User, repoName string) string {
	t.Helper()

	var got strings.Builder

	for _, a := range []string{"read", "write", "delete", "manage"} {
		ok, err := f.eval.Evaluate(context.Background(), f.principal(u), repoName, "", a)
		if err != nil {
			t.Fatalf("Evaluate(%s): %v", a, err)
		}

		if ok {
			got.WriteString(a[:1])
		}
	}

	return got.String() // letters: r, w, d, m
}

func (f *projectAuthzFixture) expect(t *testing.T, step string, u *models.User, want string) {
	t.Helper()

	if got := f.actions(t, u, f.repo.Name); got != want {
		t.Fatalf("%s: actions = %q, want %q", step, got, want)
	}
}

// newGroup creates a group and puts alice in it.
func (f *projectAuthzFixture) newGroup(t *testing.T, name string) *models.Group {
	t.Helper()

	ctx := context.Background()

	g, err := f.pg.CreateGroup(ctx, &models.Group{Name: name})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if err := f.pg.AddUserToGroup(ctx, f.alice.ID, g.ID); err != nil {
		t.Fatalf("AddUserToGroup: %v", err)
	}

	return g
}

func (f *projectAuthzFixture) attach(t *testing.T, g *models.Group, role string) {
	t.Helper()

	if err := f.pg.SetProjectGroup(context.Background(), f.project.ID, g.ID, role, &f.owner.ID); err != nil {
		t.Fatalf("SetProjectGroup(%s): %v", role, err)
	}
}

func TestProjectAuthz_MigrationApplies(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	got, err := f.pg.GetProjectByName(ctx, "acme")
	if err != nil || got == nil || got.Slug != "acme" {
		t.Fatalf("project = %+v, %v", got, err)
	}

	sys, err := f.pg.GetProject(ctx, models.SystemProjectID)
	if err != nil || sys == nil || sys.RepoNamePrefix != "" {
		t.Fatalf("system project = %+v, %v; want empty name prefix", sys, err)
	}

	if err := f.pg.AddProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleViewer); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE project_members SET role = 'admin' WHERE project_id = $1`, f.project.ID); err == nil {
		t.Fatal("project role CHECK accepted a legacy role")
	}

	// Repository names are global: the same name in another project is rejected.
	dup := &models.Repository{Name: f.repo.Name, Format: "raw", RepoType: "hosted", ProjectID: ptr(models.SystemProjectID)}
	if err := f.pg.CreateRepository(ctx, dup); !database.IsUniqueViolation(err) {
		t.Fatalf("duplicate repository name across projects: err = %v, want unique violation", err)
	}
}

func ptr[T any](v T) *T { return new(v) }

func TestProjectAuthz_MembershipLifecycle(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	f.expect(t, "non-member", f.alice, "")
	f.expect(t, "recorded owner without membership row", f.owner, "rwd")

	steps := []struct {
		role string
		want string
	}{
		{models.ProjectRoleViewer, "r"},
		{models.ProjectRoleDeveloper, "rw"},
		{models.ProjectRoleMaintainer, "rwd"},
		{models.ProjectRoleOwner, "rwd"},
		{models.ProjectRoleViewer, "r"},
	}

	if err := f.pg.AddProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleViewer); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	f.expect(t, "viewer", f.alice, "r")

	// The recorded owner needs an owner row so that alice is not the last owner.
	if err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.owner.ID, models.ProjectRoleOwner, nil); err != nil {
		t.Fatalf("owner membership: %v", err)
	}

	for _, s := range steps[1:] {
		if err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.alice.ID, s.role, nil); err != nil {
			t.Fatalf("ChangeProjectMember(%s): %v", s.role, err)
		}

		f.expect(t, "role "+s.role, f.alice, s.want)
	}

	if err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.alice.ID, "", nil); err != nil {
		t.Fatalf("remove member: %v", err)
	}

	f.expect(t, "removed member", f.alice, "")
}

// TestProjectAuthz_RemovedMemberStaleWithoutInvalidation shows the cache would
// keep serving a removed member (writes bypassing the PostgresDB hooks are
// seen only after the TTL) and that the hooked write drops it.
func TestProjectAuthz_RemovedMemberStaleWithoutInvalidation(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	if err := f.pg.AddProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleViewer); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	f.expect(t, "member (warms the cache)", f.alice, "r")

	if _, err := f.pg.GetDB().ExecContext(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, f.project.ID, f.alice.ID); err != nil {
		t.Fatalf("raw delete: %v", err)
	}

	f.expect(t, "raw delete without the invalidation hook (cached)", f.alice, "r")

	// Any hooked project write drops every cached lookup.
	if err := f.pg.UpdateProject(ctx, f.project); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	f.expect(t, "after a hooked write", f.alice, "")
}

func TestProjectAuthz_SystemProjectGrantsNothing(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	sysRepo := &models.Repository{Name: "sys-raw", Format: "raw", RepoType: "hosted", ProjectID: ptr(models.SystemProjectID)}
	if err := f.pg.CreateRepository(ctx, sysRepo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	if err := f.pg.AddProjectMember(ctx, models.SystemProjectID, f.alice.ID, models.ProjectRoleOwner); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	if got := f.actions(t, f.alice, sysRepo.Name); got != "" {
		t.Fatalf("system project owner holds %q on a system-project repository, want none", got)
	}
}

func TestProjectAuthz_RepositoryListFilter(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	sysRepo := &models.Repository{Name: "sys-raw", Format: "raw", RepoType: "hosted", ProjectID: ptr(models.SystemProjectID)}
	if err := f.pg.CreateRepository(ctx, sysRepo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	visible := func() map[string]bool {
		filter, err := f.eval.RepoReadFilter(ctx, f.principal(f.alice))
		if err != nil {
			t.Fatalf("RepoReadFilter: %v", err)
		}

		repos, err := f.pg.ListRepositories(ctx, 1, 100, "")
		if err != nil {
			t.Fatalf("ListRepositories: %v", err)
		}

		out := map[string]bool{}

		for _, r := range repos {
			if ok, err := filter(r); err != nil {
				t.Fatalf("filter: %v", err)
			} else if ok {
				out[r.Name] = true
			}
		}

		return out
	}

	if got := visible(); len(got) != 0 {
		t.Fatalf("non-member sees %v", got)
	}

	if err := f.pg.AddProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleViewer); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	if got := visible(); len(got) != 1 || !got["acme-raw"] {
		t.Fatalf("member sees %v, want only acme-raw", got)
	}
}

func TestProjectAuthz_RepositoryMoveAndProjectDelete(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	if err := f.pg.AddProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleViewer); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	f.expect(t, "member", f.alice, "r")

	// Delete is blocked while the project owns a repository.
	if err := f.pg.DeleteProject(ctx, f.project.ID); !database.IsForeignKeyViolation(err) {
		t.Fatalf("DeleteProject with repositories: err = %v, want foreign key violation", err)
	}

	f.expect(t, "after blocked delete", f.alice, "r")

	// Moving the repository to the system project removes the access.
	f.repo.ProjectID = ptr(models.SystemProjectID)
	if err := f.pg.UpdateRepository(ctx, f.repo); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	f.expect(t, "repository moved to the system project", f.alice, "")

	// And back; then deleting the repository and the project.
	f.repo.ProjectID = &f.project.ID
	if err := f.pg.UpdateRepository(ctx, f.repo); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}

	f.expect(t, "repository moved back", f.alice, "r")

	if err := f.pg.DeleteRepository(ctx, f.repo.ID); err != nil {
		t.Fatalf("DeleteRepository: %v", err)
	}

	if err := f.pg.DeleteProject(ctx, f.project.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	grants, err := f.pg.ListProjectGrantsByUser(ctx, f.alice.ID)
	if err != nil || len(grants) != 0 {
		t.Fatalf("grants after project delete = %v, %v", grants, err)
	}
}

// TestProjectAuthz_GroupRoles: a user in a group attached to the project gets
// the data access of the group's role on the project's repositories only.
func TestProjectAuthz_GroupRoles(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	other := &models.Project{Name: "other", OwnerID: &f.owner.ID}
	if err := f.pg.CreateProject(ctx, other); err != nil {
		t.Fatalf("CreateProject other: %v", err)
	}

	otherRepo := &models.Repository{Name: "other-raw", Format: "raw", RepoType: "hosted", ProjectID: &other.ID}
	if err := f.pg.CreateRepository(ctx, otherRepo); err != nil {
		t.Fatalf("CreateRepository other: %v", err)
	}

	sysRepo := &models.Repository{Name: "sys-raw", Format: "raw", RepoType: "hosted", ProjectID: ptr(models.SystemProjectID)}
	if err := f.pg.CreateRepository(ctx, sysRepo); err != nil {
		t.Fatalf("CreateRepository sys: %v", err)
	}

	g := f.newGroup(t, "team")

	f.expect(t, "in a group not attached", f.alice, "")

	for _, tt := range []struct{ role, want string }{
		{models.ProjectRoleViewer, "r"},
		{models.ProjectRoleDeveloper, "rw"},
		{models.ProjectRoleMaintainer, "rwd"},
	} {
		f.attach(t, g, tt.role)
		f.expect(t, "group role "+tt.role, f.alice, tt.want)

		if got := f.actions(t, f.alice, otherRepo.Name); got != "" {
			t.Fatalf("group role %s: actions on other project's repo = %q, want none", tt.role, got)
		}

		if got := f.actions(t, f.alice, sysRepo.Name); got != "" {
			t.Fatalf("group role %s: actions on system project's repo = %q, want none", tt.role, got)
		}
	}
}

// TestProjectAuthz_GroupMaxRoleWins: the highest role over the direct
// membership and the group wins, whichever carries it.
func TestProjectAuthz_GroupMaxRoleWins(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()
	g := f.newGroup(t, "team")

	tests := []struct{ direct, group, want string }{
		{models.ProjectRoleViewer, models.ProjectRoleMaintainer, "rwd"},
		{models.ProjectRoleDeveloper, models.ProjectRoleViewer, "rw"},
		{models.ProjectRoleMaintainer, models.ProjectRoleDeveloper, "rwd"},
		{models.ProjectRoleViewer, models.ProjectRoleDeveloper, "rw"},
	}

	for _, tt := range tests {
		name := fmt.Sprintf("direct %s + group %s", tt.direct, tt.group)

		if err := f.pg.AddProjectMember(ctx, f.project.ID, f.alice.ID, tt.direct); err != nil {
			t.Fatalf("%s: AddProjectMember: %v", name, err)
		}

		f.attach(t, g, tt.group)
		f.expect(t, name, f.alice, tt.want)

		if err := f.pg.ChangeProjectMember(ctx, f.project.ID, f.alice.ID, "", nil); err != nil {
			t.Fatalf("%s: remove member: %v", name, err)
		}

		// The group alone is still in force after the direct membership goes.
		f.expect(t, name+" (membership removed)", f.alice, map[string]string{
			models.ProjectRoleViewer: "r", models.ProjectRoleDeveloper: "rw", models.ProjectRoleMaintainer: "rwd",
		}[tt.group])
	}
}

// TestProjectAuthz_GroupRevocation: every way of ending the group grant drops
// the access immediately although the cache TTL is a minute.
func TestProjectAuthz_GroupRevocation(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		revoke func(g *models.Group) error
	}{
		{"detach group from project", func(g *models.Group) error { return f.pg.ChangeProjectGroup(ctx, f.project.ID, g.ID, "", nil, nil) }},
		{"remove user from group", func(g *models.Group) error { return f.pg.RemoveUserFromGroup(ctx, f.alice.ID, g.ID) }},
		{"delete group", func(g *models.Group) error { return f.pg.DeleteGroup(ctx, g.ID) }},
	}

	for i, tt := range tests {
		g := f.newGroup(t, fmt.Sprintf("team-%d", i))
		f.attach(t, g, models.ProjectRoleDeveloper)
		f.expect(t, tt.name+": before (warms the cache)", f.alice, "rw")

		if err := tt.revoke(g); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}

		f.expect(t, tt.name+": after", f.alice, "")
	}
}

func TestProjectAuthz_ListProjectsIncludesGroupDerived(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()

	names := func() []string {
		projects, err := f.pg.ListProjects(ctx, f.alice.ID)
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}

		out := make([]string, 0, len(projects))
		for _, p := range projects {
			out = append(out, p.Name)
		}

		return out
	}

	if got := names(); len(got) != 0 {
		t.Fatalf("ListProjects before = %v, want none", got)
	}

	g := f.newGroup(t, "team")
	f.attach(t, g, models.ProjectRoleViewer)

	if got := names(); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("ListProjects via group = %v, want [acme]", got)
	}

	// Direct membership in the same project does not duplicate the row.
	if err := f.pg.AddProjectMember(ctx, f.project.ID, f.alice.ID, models.ProjectRoleDeveloper); err != nil {
		t.Fatalf("AddProjectMember: %v", err)
	}

	if got := names(); len(got) != 1 {
		t.Fatalf("ListProjects with member+group = %v, want one row", got)
	}
}

func TestProjectAuthz_DeleteGroupCascadesAttachments(t *testing.T) {
	f := newProjectAuthzFixture(t)
	ctx := context.Background()
	g := f.newGroup(t, "team")
	f.attach(t, g, models.ProjectRoleViewer)

	if got, err := f.pg.ListProjectGroups(ctx, f.project.ID); err != nil || len(got) != 1 {
		t.Fatalf("ListProjectGroups = %v, %v; want one", got, err)
	}

	if err := f.pg.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	if got, err := f.pg.ListProjectGroups(ctx, f.project.ID); err != nil || len(got) != 0 {
		t.Fatalf("ListProjectGroups after group delete = %v, %v; want none", got, err)
	}

	var n int
	if err := f.pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM project_groups WHERE group_id = $1`, g.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("project_groups rows for deleted group = %d, %v", n, err)
	}
}

// TestProjectAuthz_CreateProjectOverHTTP is the regression for POST
// /api/v1/projects answering 500 on the real schema (slug NOT NULL was never set).
func TestProjectAuthz_CreateProjectOverHTTP(t *testing.T) {
	f := newProjectAuthzFixture(t)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	api.NewProjectsHandler(f.pg, log, f.eval, auth.NewProjectSettings(f.pg, true)).RegisterRoutes(router)

	post := func(body string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/projects", strings.NewReader(body))
		// Policy fields (max_repositories) are admin-only; the legacy key is a global admin.
		p := f.principal(f.owner)
		p.IsLegacy = true
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &p))

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)

		return rw.Code
	}

	tests := []struct {
		name string
		body string
		want int
	}{
		{"first create", `{"name":"My_Project","max_repositories":7}`, http.StatusCreated},
		{"same name again", `{"name":"My_Project"}`, http.StatusConflict},
		{"slug-only collision", `{"name":"my-project"}`, http.StatusConflict},
		{"another project", `{"name":"other"}`, http.StatusCreated},
		{"bad name", `{"name":"no spaces"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		if got := post(tt.body); got != tt.want {
			t.Fatalf("%s: status = %d, want %d", tt.name, got, tt.want)
		}
	}

	p, err := f.pg.GetProjectByName(context.Background(), "My_Project")
	if err != nil || p == nil || p.Slug != "my-project" || p.RepoNamePrefix != "my-project-" || p.MaxRepositories != 7 || p.TokenMaxTTLDays != 365 {
		t.Fatalf("stored project = %+v, %v", p, err)
	}
}
