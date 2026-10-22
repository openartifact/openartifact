// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// User IDs are UUIDs: the handlers reject any other member id. The comment on
// each is the username.
const (
	uOwner1 = "0f000000-0000-4000-8000-000000000001" // owner-1: project acme's recorded owner
	uOwner2 = "0f000000-0000-4000-8000-000000000002" // owner-2
	uMaint1 = "0f000000-0000-4000-8000-000000000011" // maint-1
	uMaint2 = "0f000000-0000-4000-8000-000000000012" // maint-2
	uDev1   = "0f000000-0000-4000-8000-000000000021" // dev-1
	uView1  = "0f000000-0000-4000-8000-000000000031" // view-1
	uNewbie = "0f000000-0000-4000-8000-000000000041" // newbie: user without any role
	uAdmin1 = "0f000000-0000-4000-8000-000000000051" // admin-1: global admin, not a member
	uPlain1 = "0f000000-0000-4000-8000-000000000061" // plain-1: user without any role
	uGroup  = "0f000000-0000-4000-8000-000000000071" // grouped: gets a role through a group
	uGhost  = "0f000000-0000-4000-8000-0000000000ff" // a well-formed id that names no user
)

// testUsernames maps the fixture user IDs to their usernames.
//
//nolint:gochecknoglobals // immutable test table
var testUsernames = map[string]string{
	uOwner1: "owner-1", uOwner2: "owner-2", uMaint1: "maint-1", uMaint2: "maint-2", uDev1: "dev-1",
	uView1: "view-1", uNewbie: "newbie", uAdmin1: "admin-1", uPlain1: "plain-1", uGroup: "grouped",
}

const (
	testAcmeID = "proj-acme"
	// testGroupID is a UUID: the handler resolves a UUID {group} by ID and any
	// other value by name.
	testGroupID   = "5b1f0e2a-1111-4222-8333-444455556666"
	testGroupName = "eng"
)

// projectsMockDB is an in-memory project store for the projects and
// repositories handlers. admins holds the user IDs that are global admins.
type projectsMockDB struct {
	repoCreateMockDB

	projects     map[string]*models.Project // by name
	members      map[string]*models.ProjectMember
	users        map[string]*models.User
	groups       map[string]*models.Group
	projectGroup map[string]*models.ProjectGroup // by group ID, project acme only
	userGroups   map[string][]string             // user ID -> group IDs
	admins       map[string]bool
	userProjects map[string][]*models.Project
	settings     map[string]string
	projectRepos []*models.Repository // live repositories of project acme, newest first

	// memberLookups counts the user and member reads and writes a handler
	// issued: a malformed id must be rejected before any of them.
	memberLookups int

	mu     sync.Mutex // guards audits (written by the audit worker goroutine)
	audits []*models.AuditLog

	createdProject *models.Project
	updated        *models.Project
	deleted        []string
	removed        []string
	roleUpdates    map[string]string
	setGroups      map[string]string
	setGroupBy     *string
	removedGroups  []string
	listedAll      bool

	repo      *models.Repository
	updRepo   *models.Repository
	deleteErr error

	tokenStore tokenStore
}

var _ database.Database = (*projectsMockDB)(nil)

func (db *projectsMockDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	if db.admins[userID] {
		return adminTargets(), nil
	}

	return nil, nil
}

func (db *projectsMockDB) GetProjectByName(_ context.Context, name string) (*models.Project, error) {
	return db.projects[name], nil
}

func (db *projectsMockDB) GetProject(_ context.Context, id string) (*models.Project, error) {
	for _, p := range db.projects {
		if p.ID == id {
			return p, nil
		}
	}

	return nil, nil
}

func (db *projectsMockDB) CreateProject(_ context.Context, project *models.Project) error {
	for _, p := range db.projects {
		if p.Name == project.Name || p.Slug == project.Slug {
			return &pgconn.PgError{Code: "23505"}
		}
	}

	project.ID = "new-project"
	db.createdProject = project
	db.projects[project.Name] = project

	return nil
}

func (db *projectsMockDB) UpdateProject(_ context.Context, project *models.Project) error {
	db.updated = project
	return nil
}

func (db *projectsMockDB) DeleteProject(_ context.Context, id string) error {
	if db.deleteErr != nil {
		return db.deleteErr
	}

	db.deleted = append(db.deleted, id)

	return nil
}

func (db *projectsMockDB) ListProjects(_ context.Context, userID string) ([]*models.Project, error) {
	return db.userProjects[userID], nil
}

func (db *projectsMockDB) ListAllProjects(context.Context) ([]*models.Project, error) {
	db.listedAll = true

	out := make([]*models.Project, 0, len(db.projects))
	for _, p := range db.projects {
		out = append(out, p)
	}

	return out, nil
}

func (db *projectsMockDB) AddProjectMember(_ context.Context, projectID, userID, role string) error {
	m := &models.ProjectMember{ID: "m-" + userID, ProjectID: projectID, UserID: userID, Role: role, CreatedAt: time.Now()}
	if u := db.users[userID]; u != nil {
		m.Username = u.Username
	}

	db.members[userID] = m

	return nil
}

func (db *projectsMockDB) GetProjectMember(_ context.Context, projectID, userID string) (*models.ProjectMember, error) {
	db.memberLookups++

	if m := db.members[userID]; m != nil && m.ProjectID == projectID {
		return m, nil
	}

	return nil, nil
}

func (db *projectsMockDB) ListProjectMembers(_ context.Context, projectID string) ([]*models.ProjectMember, error) {
	var out []*models.ProjectMember

	for _, m := range db.members {
		if m.ProjectID == projectID {
			out = append(out, m)
		}
	}

	return out, nil
}

// ChangeProjectMember mirrors PostgresDB.ChangeProjectMember: the guard sees
// the current role first, then the recorded-owner and last-owner invariants
// apply; newRole "" removes the member.
func (db *projectsMockDB) ChangeProjectMember(_ context.Context, projectID, userID, newRole string, guard database.ProjectRoleGuard) error {
	db.memberLookups++

	project, _ := db.GetProject(context.Background(), projectID)
	if project == nil {
		return database.ErrProjectNotFound
	}

	var current string
	if m := db.members[userID]; m != nil && m.ProjectID == projectID {
		current = m.Role
	}

	if guard != nil {
		if err := guard(current); err != nil {
			return err
		}
	}

	if current != "" && current != newRole {
		if project.OwnerID != nil && *project.OwnerID == userID {
			return database.ErrProjectOwnerLocked
		}

		if current == models.ProjectRoleOwner && db.ownerCount(projectID) <= 1 {
			return database.ErrLastProjectOwner
		}
	}

	switch {
	case newRole == current:
	case newRole == "":
		db.removed = append(db.removed, userID)
		delete(db.members, userID)
	case current == "":
		_ = db.AddProjectMember(context.Background(), projectID, userID, newRole)
	default:
		db.roleUpdates[userID] = newRole
		db.members[userID].Role = newRole
	}

	return nil
}

func (db *projectsMockDB) ownerCount(projectID string) int {
	n := 0

	for _, m := range db.members {
		if m.ProjectID == projectID && m.Role == models.ProjectRoleOwner {
			n++
		}
	}

	return n
}

// ChangeProjectGroup mirrors PostgresDB.ChangeProjectGroup: guard first, then
// attach, change or (newRole "") detach.
func (db *projectsMockDB) ChangeProjectGroup(ctx context.Context, projectID, groupID, newRole string, createdBy *string, guard database.ProjectRoleGuard) error {
	if project, _ := db.GetProject(ctx, projectID); project == nil {
		return database.ErrProjectNotFound
	}

	var current string
	if g := db.projectGroup[groupID]; g != nil {
		current = g.Role
	}

	if guard != nil {
		if err := guard(current); err != nil {
			return err
		}
	}

	switch newRole {
	case current:
	case "":
		db.removedGroups = append(db.removedGroups, groupID)
		delete(db.projectGroup, groupID)
	default:
		return db.SetProjectGroup(ctx, projectID, groupID, newRole, createdBy)
	}

	return nil
}

func (db *projectsMockDB) ListProjectRepositories(_ context.Context, projectID string, page, limit int) ([]*models.Repository, int, error) {
	if projectID != testAcmeID {
		return []*models.Repository{}, 0, nil
	}

	start := min((page-1)*limit, len(db.projectRepos))
	end := min(start+limit, len(db.projectRepos))

	return db.projectRepos[start:end], len(db.projectRepos), nil
}

func (db *projectsMockDB) CountRepositoriesByProject(_ context.Context, projectIDs []string) (map[string]int, error) {
	counts := map[string]int{}

	for _, id := range projectIDs {
		if id == testAcmeID && len(db.projectRepos) > 0 {
			counts[id] = len(db.projectRepos)
		}
	}

	return counts, nil
}

// ListProjectGrantsByUser derives the evaluator's grants the way the real
// query does: recorded owner, membership rows and group attachments.
func (db *projectsMockDB) ListProjectGrantsByUser(_ context.Context, userID string) ([]*models.ProjectGrant, error) {
	var grants []*models.ProjectGrant

	for _, p := range db.projects {
		if p.OwnerID != nil && *p.OwnerID == userID {
			grants = append(grants, &models.ProjectGrant{ProjectID: p.ID, Role: models.ProjectRoleOwner})
		}
	}

	for _, m := range db.members {
		if m.UserID == userID {
			grants = append(grants, &models.ProjectGrant{ProjectID: m.ProjectID, Role: m.Role})
		}
	}

	for _, gid := range db.userGroups[userID] {
		if g := db.projectGroup[gid]; g != nil {
			grants = append(grants, &models.ProjectGrant{ProjectID: g.ProjectID, Role: g.Role})
		}
	}

	return grants, nil
}

func (db *projectsMockDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	db.memberLookups++

	return db.users[id], nil
}

func (db *projectsMockDB) GetUserByUsername(_ context.Context, username string) (*models.User, error) {
	for _, u := range db.users {
		if u.Username == username {
			return u, nil
		}
	}

	return nil, nil
}

func (db *projectsMockDB) GetGroupByID(_ context.Context, id string) (*models.Group, error) {
	return db.groups[id], nil
}

func (db *projectsMockDB) GetGroupByName(_ context.Context, name string) (*models.Group, error) {
	for _, g := range db.groups {
		if g.Name == name {
			return g, nil
		}
	}

	return nil, nil
}

func (db *projectsMockDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	return db.settings[key], nil
}

func (db *projectsMockDB) SetSystemSetting(_ context.Context, key, value string) error {
	db.settings[key] = value
	return nil
}

func (db *projectsMockDB) InsertAuditLog(_ context.Context, event *models.AuditLog) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.audits = append(db.audits, event)

	return nil
}

func (db *projectsMockDB) ListProjectGroups(_ context.Context, _ string) ([]*models.ProjectGroup, error) {
	var out []*models.ProjectGroup
	for _, g := range db.projectGroup {
		out = append(out, g)
	}

	return out, nil
}

func (db *projectsMockDB) GetProjectGroup(_ context.Context, _, groupID string) (*models.ProjectGroup, error) {
	return db.projectGroup[groupID], nil
}

func (db *projectsMockDB) SetProjectGroup(_ context.Context, projectID, groupID, role string, createdBy *string) error {
	db.setGroups[groupID] = role
	db.setGroupBy = createdBy
	db.projectGroup[groupID] = &models.ProjectGroup{ProjectID: projectID, GroupID: groupID, GroupName: db.groups[groupID].Name, Role: role, CreatedAt: time.Now()}

	return nil
}

func (db *projectsMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if db.repo != nil && db.repo.Name == name {
		cp := *db.repo
		return &cp, nil
	}

	return nil, nil
}

func (db *projectsMockDB) MoveRepositoryToProject(_ context.Context, _, _ string, _ int) error {
	return nil
}

func (db *projectsMockDB) UpdateRepository(_ context.Context, repo *models.Repository) error {
	db.updRepo = repo
	return nil
}

// newProjectsMock builds project "acme" owned by owner-1 with members:
// owner-1/owner-2 (owner), maint-1/maint-2 (maintainer), dev-1 (developer),
// view-1 (viewer). admin-1 is a global admin who is not a member.
func newProjectsMock() *projectsMockDB {
	owner := uOwner1
	display := "Acme"
	desc := "old"

	acme := &models.Project{
		ID: testAcmeID, Name: "acme", Slug: "acme", OwnerID: &owner,
		DisplayName: &display, Description: &desc,
		AllowedFormats:   []string{"maven"},
		AllowedRepoTypes: []string{models.ProjectRepoTypeHosted},
		RepoNamePrefix:   "acme-", MaxRepositories: 10, TokenMaxTTLDays: 30,
	}

	db := &projectsMockDB{
		projects:     map[string]*models.Project{"acme": acme},
		members:      map[string]*models.ProjectMember{},
		users:        map[string]*models.User{},
		groups:       map[string]*models.Group{testGroupID: {ID: testGroupID, Name: testGroupName}},
		projectGroup: map[string]*models.ProjectGroup{},
		userGroups:   map[string][]string{},
		admins:       map[string]bool{uAdmin1: true},
		userProjects: map[string][]*models.Project{},
		settings:     map[string]string{},
		roleUpdates:  map[string]string{},
		setGroups:    map[string]string{},
	}

	for id, role := range map[string]string{
		uOwner1: models.ProjectRoleOwner, uOwner2: models.ProjectRoleOwner,
		uMaint1: models.ProjectRoleMaintainer, uMaint2: models.ProjectRoleMaintainer,
		uDev1: models.ProjectRoleDeveloper, uView1: models.ProjectRoleViewer,
	} {
		db.members[id] = &models.ProjectMember{ID: "m-" + id, ProjectID: testAcmeID, UserID: id, Username: testUsernames[id], Role: role}
		db.users[id] = &models.User{ID: id, Username: testUsernames[id]}
	}

	for _, id := range []string{uNewbie, uAdmin1, uPlain1} {
		db.users[id] = &models.User{ID: id, Username: testUsernames[id]}
	}

	return db
}

// newProjectsRouter wires the projects handler with the self-create default off.
func newProjectsRouter(t *testing.T, db *projectsMockDB) http.Handler {
	t.Helper()

	return newProjectsRouterDefault(t, db, false)
}

// newProjectsRouterDefault wires the handler with selfCreateDefault as the
// config default of projects.self_create (a stored setting still wins).
func newProjectsRouterDefault(t *testing.T, db *projectsMockDB, selfCreateDefault bool) http.Handler {
	t.Helper()

	log := newFixTestLogger(t)

	router := chi.NewRouter()
	NewProjectsHandler(db, log, auth.NewEvaluator(db, log, false), auth.NewProjectSettings(db, selfCreateDefault)).RegisterRoutes(router)

	return router
}

// testPrincipal is the session principal of a fixture user.
func testPrincipal(userID string) *auth.Principal {
	return &auth.Principal{UserID: &userID, Username: testUsernames[userID]}
}

// scopedPrincipal is an access token of a fixture user that carries scopes.
func scopedPrincipal(userID string) *auth.Principal {
	p := testPrincipal(userID)
	p.TokenKind = auth.TokenKindAccess
	p.TokenScopes = []string{"target-1"}

	return p
}

// deployPrincipal is a deploy token of a fixture user.
func deployPrincipal(userID string) *auth.Principal {
	p := testPrincipal(userID)
	p.TokenKind = auth.TokenKindDeploy

	return p
}

func projectJSON(t *testing.T, router http.Handler, userID, method, target, body string) (int, map[string]any) {
	t.Helper()

	return projectJSONAs(t, router, testPrincipal(userID), method, target, body)
}

// projectJSONAs sends the request as principal (nil: anonymous, no principal
// in the context) and decodes the JSON response body.
func projectJSONAs(t *testing.T, router http.Handler, principal *auth.Principal, method, target, body string) (int, map[string]any) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if principal != nil {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), principal))
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	out := map[string]any{}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	return rw.Code, out
}

func checkCreateDefaults(t *testing.T, p *models.Project) {
	t.Helper()

	if p.RepoNamePrefix != "my-proj-" || p.MaxRepositories != 50 || p.TokenMaxTTLDays != 365 {
		t.Errorf("defaults = prefix %q max %d ttl %d", p.RepoNamePrefix, p.MaxRepositories, p.TokenMaxTTLDays)
	}

	if !slices.Equal(p.AllowedRepoTypes, []string{models.ProjectRepoTypeHosted}) {
		t.Errorf("allowed_repo_types = %v, want [hosted]", p.AllowedRepoTypes)
	}

	if p.AllowedFormats != nil || p.AllowPublicRepos {
		t.Errorf("allowed_formats = %v public = %v, want nil/false", p.AllowedFormats, p.AllowPublicRepos)
	}

	if p.OwnerID == nil || *p.OwnerID != uAdmin1 {
		t.Errorf("owner = %v, want admin-1", p.OwnerID)
	}
}

func checkCreateWithPolicy(t *testing.T, p *models.Project) {
	t.Helper()

	if !slices.Equal(p.AllowedFormats, []string{"go", "raw"}) || !slices.Equal(p.AllowedRepoTypes, []string{"hosted", "proxy"}) {
		t.Errorf("lists = %v / %v", p.AllowedFormats, p.AllowedRepoTypes)
	}

	if p.RepoNamePrefix != "team_" || !p.AllowPublicRepos || p.MaxRepositories != 3 || p.TokenMaxTTLDays != 0 {
		t.Errorf("policy = %+v", p)
	}
}

func checkNilFormats(t *testing.T, p *models.Project) {
	t.Helper()

	if p.AllowedFormats != nil {
		t.Errorf("allowed_formats = %#v, want nil", p.AllowedFormats)
	}
}

func checkEmptyPrefix(t *testing.T, p *models.Project) {
	t.Helper()

	if p.RepoNamePrefix != "" {
		t.Errorf("prefix = %q, want empty", p.RepoNamePrefix)
	}
}

func checkDisplayNameNew(t *testing.T, p *models.Project) {
	t.Helper()

	if p.DisplayName == nil || *p.DisplayName != "New" {
		t.Errorf("display_name = %v", p.DisplayName)
	}
}

func checkUpdateKeepsOmitted(t *testing.T, p *models.Project) {
	t.Helper()

	if *p.Description != "d" || *p.DisplayName != "Acme" {
		t.Errorf("description/display = %q/%q", *p.Description, *p.DisplayName)
	}

	if !slices.Equal(p.AllowedFormats, []string{"maven"}) || p.RepoNamePrefix != "acme-" || p.MaxRepositories != 10 || p.TokenMaxTTLDays != 30 {
		t.Errorf("policy changed: %+v", p)
	}
}

func checkAdminPolicyUpdate(t *testing.T, p *models.Project) {
	t.Helper()

	if !slices.Equal(p.AllowedFormats, []string{"raw", "go"}) || !slices.Equal(p.AllowedRepoTypes, []string{"hosted", "group"}) ||
		p.RepoNamePrefix != "" || !p.AllowPublicRepos || p.MaxRepositories != 0 || p.TokenMaxTTLDays != 7 {
		t.Errorf("policy = %+v", p)
	}
}

func TestProjectsCreatePolicy(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		check      func(t *testing.T, p *models.Project)
	}{
		{"defaults", `{"name":"My_Proj"}`, http.StatusCreated, checkCreateDefaults},
		{
			"with policy", `{"name":"x","allowed_formats":["go","raw","go"],"allowed_repo_types":["hosted","proxy"],"repo_name_prefix":"team_","allow_public_repos":true,"max_repositories":3,"token_max_ttl_days":0}`,
			http.StatusCreated, checkCreateWithPolicy,
		},
		{"empty allowed_formats means every format", `{"name":"x","allowed_formats":[]}`, http.StatusCreated, checkNilFormats},
		{"empty prefix allowed", `{"name":"x","repo_name_prefix":""}`, http.StatusCreated, checkEmptyPrefix},
		{"unknown format", `{"name":"x","allowed_formats":["gomod"]}`, http.StatusBadRequest, nil},
		{"empty repo types", `{"name":"x","allowed_repo_types":[]}`, http.StatusBadRequest, nil},
		{"invalid repo type", `{"name":"x","allowed_repo_types":["virtual"]}`, http.StatusBadRequest, nil},
		{"bad prefix", `{"name":"x","repo_name_prefix":"a b/"}`, http.StatusBadRequest, nil},
		{"negative max", `{"name":"x","max_repositories":-1}`, http.StatusBadRequest, nil},
		{"negative ttl", `{"name":"x","token_max_ttl_days":-5}`, http.StatusBadRequest, nil},
		{"max overflow", `{"name":"x","max_repositories":4294967296}`, http.StatusBadRequest, nil},
		{"wrong type", `{"name":"x","max_repositories":"many"}`, http.StatusBadRequest, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, uAdmin1, http.MethodPost, "/api/v1/projects", tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.wantStatus, body)
			}

			if tt.check == nil {
				if db.createdProject != nil {
					t.Error("project was created despite the error")
				}

				return
			}

			tt.check(t, db.createdProject)

			if m := db.members[uAdmin1]; m == nil || m.Role != models.ProjectRoleOwner {
				t.Errorf("creator membership = %+v, want owner", m)
			}
		})
	}
}

func TestProjectsCreateResponse(t *testing.T) {
	db := newProjectsMock()
	router := newProjectsRouter(t, db)

	code, body := projectJSON(t, router, uAdmin1, http.MethodPost, "/api/v1/projects", `{"name":"Fresh"}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d; body = %v", code, body)
	}

	if body["repo_name_prefix"] != "fresh-" || body["max_repositories"] != float64(50) || body["token_max_ttl_days"] != float64(365) {
		t.Errorf("response policy = %v", body)
	}

	if body["allowed_formats"] != nil {
		t.Errorf("allowed_formats = %v, want null", body["allowed_formats"])
	}

	if _, ok := body["member_role"]; ok {
		t.Error("response still carries member_role")
	}
}

func TestProjectsCreateSlug(t *testing.T) {
	long := strings.Repeat("a", 65)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantSlug   string
	}{
		{"slug derived from name", `{"name":"My_Proj"}`, http.StatusCreated, "my-proj"},
		{"duplicate name", `{"name":"acme"}`, http.StatusConflict, ""},
		{"slug-only collision (case)", `{"name":"Acme"}`, http.StatusConflict, ""},
		{"bad name", `{"name":"no spaces"}`, http.StatusBadRequest, ""},
		{"name without letters or digits", `{"name":"---"}`, http.StatusBadRequest, ""},
		{"slug too long", `{"name":"` + long + `"}`, http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newProjectsRouter(t, newProjectsMock())

			code, body := projectJSON(t, router, uAdmin1, http.MethodPost, "/api/v1/projects", tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.wantStatus, body)
			}

			if tt.wantSlug != "" && body["slug"] != tt.wantSlug {
				t.Errorf("slug = %v, want %s", body["slug"], tt.wantSlug)
			}
		})
	}
}

func TestProjectsUpdatePolicy(t *testing.T) {
	tests := []struct {
		name       string
		user       string
		body       string
		wantStatus int
		check      func(t *testing.T, p *models.Project)
	}{
		{"owner display_name ok", uOwner1, `{"display_name":"New"}`, http.StatusOK, checkDisplayNameNew},
		{"non-recorded owner display_name ok", uOwner2, `{"display_name":"New"}`, http.StatusOK, checkDisplayNameNew},
		{"maintainer display_name 403 (owner only)", uMaint1, `{"display_name":"New"}`, http.StatusForbidden, nil},
		{"update keeps omitted fields", uOwner1, `{"description":"d"}`, http.StatusOK, checkUpdateKeepsOmitted},
		{"owner member policy 403 (allowed_formats)", uOwner1, `{"allowed_formats":["raw"]}`, http.StatusForbidden, nil},
		{"owner member policy 403 (repo types)", uOwner1, `{"allowed_repo_types":["proxy"]}`, http.StatusForbidden, nil},
		{"owner member policy 403 (prefix)", uOwner1, `{"repo_name_prefix":"x-"}`, http.StatusForbidden, nil},
		{"owner member policy 403 (public)", uOwner1, `{"allow_public_repos":true}`, http.StatusForbidden, nil},
		{"owner member policy 403 (max)", uOwner1, `{"max_repositories":999}`, http.StatusForbidden, nil},
		{"owner member policy 403 (ttl)", uOwner1, `{"token_max_ttl_days":9999}`, http.StatusForbidden, nil},
		{"policy with display_name still 403", uOwner1, `{"display_name":"x","max_repositories":1}`, http.StatusForbidden, nil},
		{"maintainer policy 403", uMaint1, `{"max_repositories":1}`, http.StatusForbidden, nil},
		{"developer 403", uDev1, `{"display_name":"x"}`, http.StatusForbidden, nil},
		{"viewer 403", uView1, `{"display_name":"x"}`, http.StatusForbidden, nil},
		{"outsider 404", uNewbie, `{"display_name":"x"}`, http.StatusNotFound, nil},
		{"admin non-member policy ok", uAdmin1, `{"allowed_formats":["raw","go"],"allowed_repo_types":["hosted","group"],"repo_name_prefix":"","allow_public_repos":true,"max_repositories":0,"token_max_ttl_days":7}`, http.StatusOK, checkAdminPolicyUpdate},
		{"admin empty allowed_formats stores nil", uAdmin1, `{"allowed_formats":[]}`, http.StatusOK, checkNilFormats},
		{"admin unknown format 400", uAdmin1, `{"allowed_formats":["nope"]}`, http.StatusBadRequest, nil},
		{"admin empty repo types 400", uAdmin1, `{"allowed_repo_types":[]}`, http.StatusBadRequest, nil},
		{"admin bad repo type 400", uAdmin1, `{"allowed_repo_types":["x"]}`, http.StatusBadRequest, nil},
		{"admin bad prefix 400", uAdmin1, `{"repo_name_prefix":"a/b"}`, http.StatusBadRequest, nil},
		{"admin negative max 400", uAdmin1, `{"max_repositories":-1}`, http.StatusBadRequest, nil},
		{"admin negative ttl 400", uAdmin1, `{"token_max_ttl_days":-1}`, http.StatusBadRequest, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodPatch, "/api/v1/projects/acme", tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.wantStatus, body)
			}

			if tt.wantStatus != http.StatusOK {
				if db.updated != nil {
					t.Error("project updated despite the error")
				}

				return
			}

			if db.updated == nil {
				t.Fatal("project not updated")
			}

			if tt.check != nil {
				tt.check(t, db.updated)
			}
		})
	}
}

func TestProjectsAdminBypassAndDelete(t *testing.T) {
	tests := []struct {
		name   string
		user   string
		method string
		target string
		body   string
		want   int
	}{
		{"admin non-member GET", uAdmin1, http.MethodGet, "/api/v1/projects/acme", "", http.StatusOK},
		{"admin non-member PATCH", uAdmin1, http.MethodPatch, "/api/v1/projects/acme", `{"display_name":"x"}`, http.StatusOK},
		{"admin non-member DELETE", uAdmin1, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusNoContent},
		{"admin non-member lists members", uAdmin1, http.MethodGet, "/api/v1/projects/acme/members", "", http.StatusOK},
		{"outsider GET", uNewbie, http.MethodGet, "/api/v1/projects/acme", "", http.StatusNotFound},
		{"outsider DELETE", uNewbie, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusNotFound},
		{"viewer GET", uView1, http.MethodGet, "/api/v1/projects/acme", "", http.StatusOK},
		{"viewer DELETE", uView1, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusForbidden},
		{"maintainer DELETE", uMaint1, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusForbidden},
		{"non-recorded owner DELETE", uOwner2, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusNoContent},
		{"recorded owner DELETE", uOwner1, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusNoContent},
		{"unknown project", uAdmin1, http.MethodGet, "/api/v1/projects/ghost", "", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, tt.method, tt.target, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.method == http.MethodDelete && (tt.want == http.StatusNoContent) != (len(db.deleted) == 1) {
				t.Errorf("deleted = %v", db.deleted)
			}
		})
	}
}

func TestProjectsDeleteStillOwnsRepositories(t *testing.T) {
	db := newProjectsMock()
	db.deleteErr = &pgconn.PgError{Code: "23503"}
	router := newProjectsRouter(t, db)

	code, body := projectJSON(t, router, uOwner1, http.MethodDelete, "/api/v1/projects/acme", "")
	if code != http.StatusConflict || body["code"] != "conflict" {
		t.Fatalf("status/body = %d/%v, want 409/conflict", code, body)
	}

	if msg, _ := body["error"].(string); !strings.Contains(msg, "in the trash") {
		t.Errorf("message %q does not mention repositories in the trash", msg)
	}
}

func TestProjectsList(t *testing.T) {
	tests := []struct {
		name      string
		user      string
		target    string
		want      int
		wantAll   bool
		wantNames []string
		wantRole  string
		canCreate bool
	}{
		{"member list", uDev1, "/api/v1/projects", http.StatusOK, false, []string{"acme"}, "developer", false},
		{"recorded owner list", uOwner1, "/api/v1/projects", http.StatusOK, false, []string{"acme"}, "owner", false},
		{"outsider list is empty", uNewbie, "/api/v1/projects", http.StatusOK, false, []string{}, "", false},
		{"all=true non-admin", uDev1, "/api/v1/projects?all=true", http.StatusForbidden, false, nil, "", false},
		{"all=true owner member", uOwner1, "/api/v1/projects?all=true", http.StatusForbidden, false, nil, "", false},
		{"all=true admin", uAdmin1, "/api/v1/projects?all=true", http.StatusOK, true, []string{"acme"}, "owner", true},
		{"all=false admin lists own", uAdmin1, "/api/v1/projects?all=false", http.StatusOK, false, []string{}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			for _, id := range []string{uDev1, uOwner1} {
				db.userProjects[id] = []*models.Project{db.projects["acme"]}
			}

			db.projectRepos = []*models.Repository{{ID: "r1", Name: "acme-a"}, {ID: "r2", Name: "acme-b"}}
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodGet, tt.target, "")
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if db.listedAll != tt.wantAll {
				t.Errorf("ListAllProjects called = %v, want %v", db.listedAll, tt.wantAll)
			}

			if tt.wantNames == nil {
				return
			}

			list, _ := body["projects"].([]any)
			if len(list) != len(tt.wantNames) {
				t.Fatalf("projects = %v, want %v", list, tt.wantNames)
			}

			if body["can_create"] != tt.canCreate {
				t.Errorf("can_create = %v, want %v", body["can_create"], tt.canCreate)
			}

			for i, item := range list {
				p, _ := item.(map[string]any)
				if p["name"] != tt.wantNames[i] || p["my_role"] != tt.wantRole || p["repository_count"] != float64(2) || p["is_system"] != false {
					t.Errorf("project = %v, want %s as %s with 2 repositories", p, tt.wantNames[i], tt.wantRole)
				}
			}
		})
	}
}

func TestProjectsAddMember(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		body     string
		want     int
		wantRole string
	}{
		{"owner grants owner", uOwner1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"owner"}`, http.StatusCreated, "owner"},
		{"owner grants maintainer", uOwner1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"maintainer"}`, http.StatusCreated, "maintainer"},
		{"maintainer grants maintainer", uMaint1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"maintainer"}`, http.StatusForbidden, ""},
		{"maintainer grants owner", uMaint1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"owner"}`, http.StatusForbidden, ""},
		{"maintainer grants developer", uMaint1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"developer"}`, http.StatusCreated, "developer"},
		{"maintainer grants viewer", uMaint1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"viewer"}`, http.StatusCreated, "viewer"},
		{"role defaults to viewer", uMaint1, `{"user_id":"0f000000-0000-4000-8000-000000000041"}`, http.StatusCreated, "viewer"},
		{"developer cannot add", uDev1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"viewer"}`, http.StatusForbidden, ""},
		{"viewer cannot add", uView1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"viewer"}`, http.StatusForbidden, ""},
		{"global admin grants owner", uAdmin1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"owner"}`, http.StatusCreated, "owner"},
		{"old admin role rejected", uOwner1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"admin"}`, http.StatusBadRequest, ""},
		{"old member role rejected", uOwner1, `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"member"}`, http.StatusBadRequest, ""},
		{"unknown user", uOwner1, `{"user_id":"0f000000-0000-4000-8000-0000000000ff","role":"viewer"}`, http.StatusNotFound, ""},
		{"already a member", uOwner1, `{"user_id":"0f000000-0000-4000-8000-000000000021","role":"viewer"}`, http.StatusConflict, ""},
		{"invalid body", uOwner1, `nope`, http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodPost, "/api/v1/projects/acme/members", tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.wantRole == "" {
				if m := db.members[uNewbie]; m != nil {
					t.Errorf("member added despite error: %+v", m)
				}

				return
			}

			if body["role"] != tt.wantRole || db.members[uNewbie].Role != tt.wantRole {
				t.Errorf("role = %v / %v, want %s", body["role"], db.members[uNewbie], tt.wantRole)
			}
		})
	}
}

// dropMember removes a member row, leaving the recorded owner and everybody
// else untouched.
func dropMember(db *projectsMockDB, id string) { delete(db.members, id) }

func TestProjectsUpdateMemberRole(t *testing.T) {
	tests := []struct {
		name   string
		user   string
		target string
		body   string
		setup  func(db *projectsMockDB)
		want   int
	}{
		{"owner promotes developer to maintainer", uOwner1, uDev1, `{"role":"maintainer"}`, nil, http.StatusOK},
		{"owner grants owner", uOwner1, uDev1, `{"role":"owner"}`, nil, http.StatusOK},
		{"owner demotes maintainer", uOwner1, uMaint1, `{"role":"viewer"}`, nil, http.StatusOK},
		{"owner demotes another owner", uOwner1, uOwner2, `{"role":"developer"}`, nil, http.StatusOK},
		{"maintainer grants maintainer", uMaint1, uDev1, `{"role":"maintainer"}`, nil, http.StatusForbidden},
		{"maintainer grants owner", uMaint1, uDev1, `{"role":"owner"}`, nil, http.StatusForbidden},
		{"maintainer lowers developer to viewer", uMaint1, uDev1, `{"role":"viewer"}`, nil, http.StatusOK},
		{"maintainer raises viewer to developer", uMaint1, uView1, `{"role":"developer"}`, nil, http.StatusOK},
		{"maintainer cannot demote maintainer", uMaint1, uMaint2, `{"role":"developer"}`, nil, http.StatusForbidden},
		{"maintainer cannot demote owner", uMaint1, uOwner2, `{"role":"developer"}`, nil, http.StatusForbidden},
		{"maintainer cannot touch a maintainer even to the same role", uMaint1, uMaint2, `{"role":"maintainer"}`, nil, http.StatusForbidden},
		{"owner same role is a no-op", uOwner1, uMaint2, `{"role":"maintainer"}`, nil, http.StatusOK},
		{"developer cannot change", uDev1, uView1, `{"role":"developer"}`, nil, http.StatusForbidden},
		{"recorded owner is immutable", uOwner2, uOwner1, `{"role":"maintainer"}`, nil, http.StatusConflict},
		{"global admin cannot demote recorded owner", uAdmin1, uOwner1, `{"role":"viewer"}`, nil, http.StatusConflict},
		{"last owner cannot be demoted", uOwner1, uOwner2, `{"role":"developer"}`, func(db *projectsMockDB) { dropMember(db, uOwner1) }, http.StatusConflict},
		{"invalid role admin", uOwner1, uDev1, `{"role":"admin"}`, nil, http.StatusBadRequest},
		{"invalid role member", uOwner1, uDev1, `{"role":"member"}`, nil, http.StatusBadRequest},
		{"empty role", uOwner1, uDev1, `{}`, nil, http.StatusBadRequest},
		{"unknown member", uOwner1, uGhost, `{"role":"viewer"}`, nil, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			if tt.setup != nil {
				tt.setup(db)
			}

			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodPatch, "/api/v1/projects/acme/members/"+tt.target, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.want >= http.StatusBadRequest && len(db.roleUpdates) != 0 {
				t.Errorf("role updated despite the error: %v", db.roleUpdates)
			}
		})
	}
}

func TestProjectsRemoveMemberProtectsOwners(t *testing.T) {
	tests := []struct {
		name   string
		user   string
		target string
		setup  func(db *projectsMockDB)
		want   int
	}{
		{"owner removes maintainer", uOwner1, uMaint1, nil, http.StatusNoContent},
		{"owner removes developer", uOwner1, uDev1, nil, http.StatusNoContent},
		{"owner removes another owner", uOwner1, uOwner2, nil, http.StatusNoContent},
		{"owner cannot remove the recorded owner", uOwner2, uOwner1, nil, http.StatusConflict},
		{"recorded owner cannot remove themselves", uOwner1, uOwner1, nil, http.StatusConflict},
		{"global admin cannot remove recorded owner", uAdmin1, uOwner1, nil, http.StatusConflict},
		{"global admin removes a second owner", uAdmin1, uOwner2, nil, http.StatusNoContent},
		{"last owner cannot be removed", uOwner1, uOwner2, func(db *projectsMockDB) { dropMember(db, uOwner1) }, http.StatusConflict},
		{"maintainer removes developer", uMaint1, uDev1, nil, http.StatusNoContent},
		{"maintainer removes viewer", uMaint1, uView1, nil, http.StatusNoContent},
		{"maintainer cannot remove maintainer", uMaint1, uMaint2, nil, http.StatusForbidden},
		{"maintainer cannot remove owner", uMaint1, uOwner2, nil, http.StatusForbidden},
		{"developer cannot remove anyone", uDev1, uView1, nil, http.StatusForbidden},
		{"developer cannot remove maintainer", uDev1, uMaint1, nil, http.StatusForbidden},
		{"unknown member", uOwner1, uGhost, nil, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			if tt.setup != nil {
				tt.setup(db)
			}

			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodDelete, "/api/v1/projects/acme/members/"+tt.target, "")
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if (tt.want == http.StatusNoContent) != slices.Equal(db.removed, []string{tt.target}) {
				t.Errorf("removed = %v", db.removed)
			}
		})
	}
}

func TestProjectsGroupsList(t *testing.T) {
	tests := []struct {
		name string
		user string
		want int
	}{
		{"viewer lists", uView1, http.StatusOK},
		{"admin non-member lists", uAdmin1, http.StatusOK},
		{"outsider hidden", uNewbie, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			db.projectGroup[testGroupID] = &models.ProjectGroup{ProjectID: testAcmeID, GroupID: testGroupID, GroupName: testGroupName, Role: models.ProjectRoleDeveloper}
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodGet, "/api/v1/projects/acme/groups", "")
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.want != http.StatusOK {
				return
			}

			list, _ := body["groups"].([]any)
			if len(list) != 1 {
				t.Fatalf("groups = %v", body["groups"])
			}

			g, _ := list[0].(map[string]any)
			if g["group_name"] != "eng" || g["role"] != "developer" {
				t.Errorf("group = %v", g)
			}
		})
	}
}

func TestProjectsSetGroup(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		group    string
		body     string
		existing *models.ProjectGroup
		want     int
		wantRole string
	}{
		{"maintainer attaches viewer", uMaint1, testGroupID, `{"role":"viewer"}`, nil, http.StatusOK, "viewer"},
		{"maintainer attaches developer", uMaint1, testGroupID, `{"role":"developer"}`, nil, http.StatusOK, "developer"},
		{"maintainer cannot attach maintainer", uMaint1, testGroupID, `{"role":"maintainer"}`, nil, http.StatusForbidden, ""},
		{"maintainer attaches by name", uMaint1, testGroupName, `{"role":"developer"}`, nil, http.StatusOK, "developer"},
		{"owner attaches maintainer", uOwner1, testGroupID, `{"role":"maintainer"}`, nil, http.StatusOK, "maintainer"},
		{"owner attaches maintainer by name", uOwner1, testGroupName, `{"role":"maintainer"}`, nil, http.StatusOK, "maintainer"},
		{"maintainer cannot alter a maintainer group", uMaint1, testGroupID, `{"role":"viewer"}`, &models.ProjectGroup{ProjectID: testAcmeID, GroupID: testGroupID, Role: models.ProjectRoleMaintainer}, http.StatusForbidden, ""},
		{"owner alters a maintainer group", uOwner1, testGroupID, `{"role":"viewer"}`, &models.ProjectGroup{ProjectID: testAcmeID, GroupID: testGroupID, Role: models.ProjectRoleMaintainer}, http.StatusOK, "viewer"},
		{"maintainer alters a developer group", uMaint1, testGroupID, `{"role":"viewer"}`, &models.ProjectGroup{ProjectID: testAcmeID, GroupID: testGroupID, Role: models.ProjectRoleDeveloper}, http.StatusOK, "viewer"},
		{"global admin attaches viewer", uAdmin1, testGroupID, `{"role":"viewer"}`, nil, http.StatusOK, "viewer"},
		{"groups never owner", uOwner1, testGroupID, `{"role":"owner"}`, nil, http.StatusBadRequest, ""},
		{"old admin role", uOwner1, testGroupID, `{"role":"admin"}`, nil, http.StatusBadRequest, ""},
		{"empty role", uOwner1, testGroupID, `{}`, nil, http.StatusBadRequest, ""},
		{"unknown group", uOwner1, "g-ghost", `{"role":"viewer"}`, nil, http.StatusNotFound, ""},
		{"developer cannot attach", uDev1, testGroupID, `{"role":"viewer"}`, nil, http.StatusForbidden, ""},
		{"outsider cannot attach (hidden)", uNewbie, testGroupID, `{"role":"viewer"}`, nil, http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			if tt.existing != nil {
				cp := *tt.existing
				db.projectGroup[tt.existing.GroupID] = &cp
			}

			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodPut, "/api/v1/projects/acme/groups/"+tt.group, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.wantRole == "" {
				if len(db.setGroups) != 0 {
					t.Errorf("group set despite the error: %v", db.setGroups)
				}

				return
			}

			if body["role"] != tt.wantRole || db.setGroups[testGroupID] != tt.wantRole {
				t.Errorf("role = %v / %v, want %s", body["role"], db.setGroups, tt.wantRole)
			}

			if db.setGroupBy == nil || *db.setGroupBy != tt.user {
				t.Errorf("created_by = %v, want %s", db.setGroupBy, tt.user)
			}
		})
	}
}

func TestProjectsRemoveGroup(t *testing.T) {
	tests := []struct {
		name string
		user string
		role string
		want int
	}{
		{"maintainer removes developer group", uMaint1, models.ProjectRoleDeveloper, http.StatusNoContent},
		{"owner removes maintainer group", uOwner1, models.ProjectRoleMaintainer, http.StatusNoContent},
		{"maintainer cannot remove maintainer group", uMaint1, models.ProjectRoleMaintainer, http.StatusForbidden},
		{"developer cannot remove", uDev1, models.ProjectRoleViewer, http.StatusForbidden},
		{"outsider hidden", uNewbie, models.ProjectRoleViewer, http.StatusNotFound},
		{"global admin removes", uAdmin1, models.ProjectRoleMaintainer, http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			db.projectGroup[testGroupID] = &models.ProjectGroup{ProjectID: testAcmeID, GroupID: testGroupID, Role: tt.role}
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodDelete, "/api/v1/projects/acme/groups/"+testGroupID, "")
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if (tt.want == http.StatusNoContent) != (len(db.removedGroups) == 1) {
				t.Errorf("removed groups = %v", db.removedGroups)
			}
		})
	}

	t.Run("group not attached", func(t *testing.T) {
		router := newProjectsRouter(t, newProjectsMock())

		if code, body := projectJSON(t, router, uOwner1, http.MethodDelete, "/api/v1/projects/acme/groups/"+testGroupID, ""); code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body = %v", code, body)
		}
	})
}

func TestRepositoriesProjectAssignment(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		target        string
		body          string
		wantStatus    int
		wantProjectID string // project_id stored; "" when nothing is written
		wantProject   string // project in the response
	}{
		{"create defaults to system project", http.MethodPost, "/api/v1/repositories", `{"name":"r","format":"raw"}`, http.StatusCreated, models.SystemProjectID, ""},
		{"create in named project", http.MethodPost, "/api/v1/repositories", `{"name":"r","format":"raw","project":"acme"}`, http.StatusCreated, testAcmeID, "acme"},
		{"create in unknown project", http.MethodPost, "/api/v1/repositories", `{"name":"r","format":"raw","project":"ghost"}`, http.StatusNotFound, "", ""},
		{"move to named project", http.MethodPatch, "/api/v1/repository/old", `{"project":"acme"}`, http.StatusOK, testAcmeID, "acme"},
		{"move to unknown project", http.MethodPatch, "/api/v1/repository/old", `{"project":"ghost"}`, http.StatusNotFound, "", ""},
		{"patch without project keeps it", http.MethodPatch, "/api/v1/repository/old", `{"description":"d"}`, http.StatusOK, models.SystemProjectID, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			sys := models.SystemProjectID
			db.repo = &models.Repository{ID: "repo-old", Name: "old", Format: "raw", RepoType: "hosted", ProjectID: &sys}

			log := newFixTestLogger(t)
			h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))
			router := chi.NewRouter()
			h.RegisterAuthed(router)

			code, body := projectJSON(t, router, uAdmin1, tt.method, tt.target, tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.wantStatus, body)
			}

			if tt.wantProjectID == "" {
				return
			}

			stored := db.created
			if tt.method == http.MethodPatch {
				stored = db.updRepo
			}

			if stored == nil || stored.ProjectID == nil || *stored.ProjectID != tt.wantProjectID {
				t.Fatalf("stored project_id = %v, want %s", stored, tt.wantProjectID)
			}

			if got, _ := body["project"].(string); got != tt.wantProject {
				t.Errorf("response project = %q, want %q", got, tt.wantProject)
			}

			if _, ok := body["org"]; ok {
				t.Error("response still carries org")
			}
		})
	}
}
