// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const acmeProjectID = "proj-acme"

// projectRBACDB serves the projects "acme" and "beta" and the project grants of user-1.
type projectRBACDB struct {
	benchRBACDB

	grants      []*models.ProjectGrant
	selfCreate  string
	settingsErr error
}

func (d *projectRBACDB) GetProjectByName(_ context.Context, name string) (*models.Project, error) {
	switch name {
	case "acme":
		return &models.Project{ID: acmeProjectID, Name: "acme"}, nil
	case "beta":
		return &models.Project{ID: "proj-beta", Name: "beta"}, nil
	}

	return nil, nil
}

func (d *projectRBACDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return d.grants, nil
}

func (d *projectRBACDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	if key != auth.SettingProjectsSelfCreate {
		return "", nil
	}

	return d.selfCreate, d.settingsErr
}

type projectCaller struct {
	kind   string // token kind; "" = session
	scopes []string
	admin  bool
	role   string // role in acme, "" = non-member
}

// projectStatus runs one request through RBACMiddleware as caller and returns
// the status code; 200 means the middleware let it through.
func projectStatus(t *testing.T, caller projectCaller, selfCreate, method, path string) int {
	t.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	targets := []*models.PermissionTarget{{ID: "t-ro", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read"}}}
	if caller.admin {
		targets = []*models.PermissionTarget{{ID: "t-admin", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"manage", "read", "write", "delete"}}}
	}

	db := &projectRBACDB{
		targets:    targets,
		repos:      map[string]*models.Repository{},
		tokens:     map[string]*models.Token{},
		users:      map[string]*models.User{"user-1": {ID: "user-1", Username: "jane"}},
		sessions:   map[string]*models.Session{},
		selfCreate: selfCreate,
	}
	if caller.role != "" {
		db.grants = []*models.ProjectGrant{{ProjectID: acmeProjectID, Role: caller.role}}
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)

	if caller.kind == "" {
		value, sess, err := auth.CreateSession(t.Context(), db, "user-1", "", "")
		if err != nil {
			t.Fatal(err)
		}

		db.sessions[sess.TokenHash] = sess
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: value})
	} else {
		raw, tok, err := auth.CreateToken(t.Context(), db, caller.kind, "ci", "user-1", caller.scopes, nil)
		if err != nil {
			t.Fatal(err)
		}

		now := time.Now()
		tok.LastUsedAt = &now
		db.tokens[tok.TokenHash] = tok
		req.Header.Set("Authorization", "Bearer "+raw)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cfg := RBACConfig{ProjectSettings: auth.NewProjectSettings(db, false)}
	handler := RBACMiddleware(cfg, db, auth.NewEvaluator(db, log, false), log)(next)

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	return rw.Code
}

// TestRBACProjectRouteMatrix drives every project route through the real
// RBACMiddleware for each role: 404 without any role (also for unknown
// projects), 403 with a role that is too low, 200 otherwise. Global admins
// pass every route.
func TestRBACProjectRouteMatrix(t *testing.T) {
	const (
		no   = http.StatusNotFound
		deny = http.StatusForbidden
		ok   = http.StatusOK
	)

	type expect struct{ none, viewer, developer, maintainer, owner, admin int }

	read := expect{no, ok, ok, ok, ok, ok}
	members := expect{no, deny, deny, ok, ok, ok}
	owner := expect{no, deny, deny, deny, ok, ok}

	routes := []struct {
		method, path string
		want         expect
	}{
		{http.MethodGet, "/api/v1/projects/acme", read},
		{http.MethodPatch, "/api/v1/projects/acme", owner},
		{http.MethodDelete, "/api/v1/projects/acme", owner},
		{http.MethodGet, "/api/v1/projects/acme/repositories", read},
		{http.MethodGet, "/api/v1/projects/acme/members", read},
		{http.MethodPost, "/api/v1/projects/acme/members", members},
		{http.MethodPatch, "/api/v1/projects/acme/members/u1", members},
		{http.MethodDelete, "/api/v1/projects/acme/members/u1", members},
		{http.MethodGet, "/api/v1/projects/acme/groups", read},
		{http.MethodPut, "/api/v1/projects/acme/groups/g1", members},
		{http.MethodDelete, "/api/v1/projects/acme/groups/g1", members},
		// Unknown shapes are global manage: only admins pass, a plain 403
		// even for owners (no project existence is leaked either).
		{http.MethodGet, "/api/v1/projects/acme/tokens", read},
		{http.MethodPost, "/api/v1/projects/acme/tokens", read},
		{http.MethodDelete, "/api/v1/projects/acme/tokens/t1", read},
		{http.MethodPut, "/api/v1/projects/acme/tokens", expect{deny, deny, deny, deny, deny, ok}},
		{http.MethodPut, "/api/v1/projects/acme", expect{deny, deny, deny, deny, deny, ok}},
		{http.MethodGet, "/api/v1/projects/a%2Fb", expect{deny, deny, deny, deny, deny, ok}},
	}

	callers := []struct {
		name   string
		caller projectCaller
		pick   func(expect) int
	}{
		{"non-member", projectCaller{}, func(e expect) int { return e.none }},
		{"viewer", projectCaller{role: models.ProjectRoleViewer}, func(e expect) int { return e.viewer }},
		{"developer", projectCaller{role: models.ProjectRoleDeveloper}, func(e expect) int { return e.developer }},
		{"maintainer", projectCaller{role: models.ProjectRoleMaintainer}, func(e expect) int { return e.maintainer }},
		{"owner", projectCaller{role: models.ProjectRoleOwner}, func(e expect) int { return e.owner }},
		{"admin", projectCaller{admin: true}, func(e expect) int { return e.admin }},
		{"admin access token", projectCaller{admin: true, kind: auth.TokenKindAccess}, func(e expect) int { return e.admin }},
		{"owner access token", projectCaller{role: models.ProjectRoleOwner, kind: auth.TokenKindAccess}, func(e expect) int { return e.owner }},
	}

	for _, c := range callers {
		for _, route := range routes {
			t.Run(c.name+" "+route.method+" "+route.path, func(t *testing.T) {
				if got, want := projectStatus(t, c.caller, "", route.method, route.path), c.pick(route.want); got != want {
					t.Fatalf("status = %d, want %d", got, want)
				}
			})
		}
	}
}

// TestRBACProjectUnknownProjectIsHidden: a project that does not exist looks
// exactly like one the caller is not a member of.
func TestRBACProjectUnknownProjectIsHidden(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/projects/ghost"},
		{http.MethodPatch, "/api/v1/projects/ghost"},
		{http.MethodPost, "/api/v1/projects/ghost/members"},
	} {
		if got := projectStatus(t, projectCaller{role: models.ProjectRoleOwner}, "", route.method, route.path); got != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", route.method, route.path, got)
		}
	}
}

// TestRBACProjectOtherProjectRoleDoesNotCarryOver: an owner of one project is
// a non-member of another.
func TestRBACProjectOtherProjectRoleDoesNotCarryOver(t *testing.T) {
	owner := projectCaller{role: models.ProjectRoleOwner}

	for _, path := range []string{"/api/v1/projects/acme", "/api/v1/projects/beta"} {
		want := http.StatusOK
		if path == "/api/v1/projects/beta" {
			want = http.StatusNotFound
		}

		if got := projectStatus(t, owner, "", http.MethodGet, path); got != want {
			t.Errorf("GET %s = %d, want %d", path, got, want)
		}
	}

	if got := projectStatus(t, owner, "", http.MethodDelete, "/api/v1/projects/beta"); got != http.StatusNotFound {
		t.Errorf("DELETE beta = %d, want 404", got)
	}
}

// TestRBACProjectsRoot covers GET/POST /api/v1/projects: listing is open to
// every authenticated non-deploy caller, creation needs admin or the
// projects.self_create setting and a human, unscoped account.
func TestRBACProjectsRoot(t *testing.T) {
	user := projectCaller{}
	scoped := projectCaller{kind: auth.TokenKindAccess, scopes: []string{"read"}}
	access := projectCaller{kind: auth.TokenKindAccess}
	deploy := projectCaller{kind: auth.TokenKindDeploy}
	admin := projectCaller{admin: true}
	adminDeploy := projectCaller{admin: true, kind: auth.TokenKindDeploy}

	tests := []struct {
		name       string
		caller     projectCaller
		selfCreate string
		method     string
		want       int
	}{
		{"user lists", user, "", http.MethodGet, http.StatusOK},
		{"deploy token cannot list", deploy, "", http.MethodGet, http.StatusForbidden},
		{"admin deploy token cannot list", adminDeploy, "", http.MethodGet, http.StatusForbidden},
		{"user creates with self_create off", user, "", http.MethodPost, http.StatusForbidden},
		{"user creates with self_create false", user, "false", http.MethodPost, http.StatusForbidden},
		{"user creates with self_create malformed fails closed", user, "maybe", http.MethodPost, http.StatusForbidden},
		{"user creates with self_create on", user, "true", http.MethodPost, http.StatusOK},
		{"access token creates with self_create on", access, "true", http.MethodPost, http.StatusOK},
		{"scoped token cannot self-create", scoped, "true", http.MethodPost, http.StatusForbidden},
		{"deploy token cannot self-create", deploy, "true", http.MethodPost, http.StatusForbidden},
		{"admin creates with self_create off", admin, "", http.MethodPost, http.StatusOK},
		{"admin deploy token cannot create", adminDeploy, "", http.MethodPost, http.StatusForbidden},
		{"unexpected method is global manage (user)", user, "true", http.MethodPut, http.StatusForbidden},
		{"unexpected method is global manage (admin)", admin, "", http.MethodPut, http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectStatus(t, tc.caller, tc.selfCreate, tc.method, "/api/v1/projects"); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestRBACProjectSettingsLookupFailure: when the self_create setting cannot be
// read, a non-admin create is answered with a 500, never allowed.
func TestRBACProjectSettingsLookupFailure(t *testing.T) {
	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	db := &projectRBACDB{
		repos:       map[string]*models.Repository{},
		tokens:      map[string]*models.Token{},
		users:       map[string]*models.User{"user-1": {ID: "user-1", Username: "jane"}},
		sessions:    map[string]*models.Session{},
		settingsErr: context.DeadlineExceeded,
	}

	value, sess, err := auth.CreateSession(t.Context(), db, "user-1", "", "")
	if err != nil {
		t.Fatal(err)
	}

	db.sessions[sess.TokenHash] = sess

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/projects", http.NoBody)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: value})

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := RBACMiddleware(RBACConfig{ProjectSettings: auth.NewProjectSettings(db, true)}, db, auth.NewEvaluator(db, log, false), log)(next)

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rw.Code, strconv.Quote(rw.Body.String()))
	}
}

func TestIsSelfServiceRequestRejectsProjectRoutes(t *testing.T) {
	uid := "user-1"
	session := &auth.Principal{UserID: &uid, Username: "u", Credential: auth.CredentialSession}

	for _, path := range []string{
		"/api/v1/projects", "/api/v1/projects/acme", "/api/v1/projects/acme/members",
		"/api/v1/projects/acme/members/user-1", "/api/v1/projects/acme/groups/g1",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
			if isSelfServiceRequest(req, session) {
				t.Errorf("isSelfServiceRequest(%s %s) = true, want false", method, path)
			}
		}
	}
}

func (d *projectRBACDB) CreateProjectToken(_ context.Context, spec database.ProjectTokenSpec) (*models.Token, error) {
	return &models.Token{
		ID: "ptok-1", UserID: "svc-1", Name: spec.Name, Kind: auth.TokenKindDeploy, TokenHash: spec.TokenHash,
		ProjectID: &spec.ProjectID, ProjectRole: &spec.Role,
	}, nil
}

func ptSpec(projectID string) database.ProjectTokenSpec {
	return database.ProjectTokenSpec{ProjectID: projectID, Name: "ci", Role: models.ProjectRoleDeveloper, CreatedBy: "user-1"}
}
