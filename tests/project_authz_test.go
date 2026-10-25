// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	pzAcme = "proj-acme"
	pzBeta = "proj-beta"
	// pzGroupEng is a UUID: the {group} route segment takes an ID or a name.
	pzGroupEng = "3f1b2c4d-0000-4000-8000-0000000000e1"
)

// pzDB is an in-memory store behind the real RBAC middleware and the real
// projects handler. admins are the user IDs holding a manage permission
// target; members is keyed projectID -> userID -> role.
type pzDB struct {
	databasetest.BaseMockDB

	mu       sync.Mutex
	admins   map[string]bool
	users    map[string]*models.User
	projects map[string]*models.Project
	members  map[string]map[string]string
	groups   map[string]*models.Group
	pgroups  map[string]map[string]string
	sessions map[string]*models.Session
	tokens   map[string]*models.Token
	settings map[string]string
	audits   []*models.AuditLog // newest first
}

// ListAuditLogs mimics the project filter of the Postgres query: only rows of
// params.ProjectID, newest first, keyset after params.After.
func (d *pzDB) ListAuditLogs(_ context.Context, params models.AuditListParams) (*models.AuditPage, error) {
	var rows []*models.AuditLog

	skipping := params.After != nil

	for _, l := range d.audits {
		if skipping {
			skipping = l.ID != params.After.ID

			continue
		}

		if params.ProjectID == "" || (l.ProjectID != nil && *l.ProjectID == params.ProjectID) {
			rows = append(rows, l)
		}
	}

	page := &models.AuditPage{Logs: rows}
	if len(rows) > params.Limit {
		page.Logs = rows[:params.Limit]
		last := page.Logs[params.Limit-1]
		page.Next = &models.AuditCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	return page, nil
}

func (d *pzDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	if d.admins[userID] {
		return []*models.PermissionTarget{{ID: "t-admin", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"manage", "read", "write", "delete"}}}, nil
	}

	return nil, nil
}

func (d *pzDB) GetPermissionTargetByName(context.Context, string) (*models.PermissionTarget, error) {
	return nil, nil
}

func (d *pzDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return nil, nil
}

func (d *pzDB) GetTokenByHash(_ context.Context, hash string) (*models.Token, error) {
	return d.tokens[hash], nil
}

func (d *pzDB) CreateToken(_ context.Context, t *models.Token) (*models.Token, error) {
	t.ID = "tok-" + t.Name
	return t, nil
}

func (d *pzDB) GetSessionByTokenHash(_ context.Context, hash string) (*models.Session, error) {
	return d.sessions[hash], nil
}

func (d *pzDB) CreateSession(_ context.Context, s *models.Session) (*models.Session, error) {
	s.ID = "sess-" + s.UserID
	return s, nil
}

func (d *pzDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return d.users[id], nil
}

func (d *pzDB) GetUserByUsername(_ context.Context, name string) (*models.User, error) {
	for _, u := range d.users {
		if u.Username == name {
			return u, nil
		}
	}

	return nil, nil
}

func (d *pzDB) ListGroupsByUser(context.Context, string) ([]*models.Group, error) { return nil, nil }

func (d *pzDB) GetGroupByName(_ context.Context, name string) (*models.Group, error) {
	for _, g := range d.groups {
		if g.Name == name {
			return g, nil
		}
	}

	return nil, nil
}

func (d *pzDB) GetGroupByID(_ context.Context, id string) (*models.Group, error) {
	return d.groups[id], nil
}

func (d *pzDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.settings[key], nil
}

func (d *pzDB) SetSystemSetting(_ context.Context, key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.settings[key] = value

	return nil
}

func (d *pzDB) GetProjectByName(_ context.Context, name string) (*models.Project, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, p := range d.projects {
		if p.Name == name {
			c := *p
			return &c, nil
		}
	}

	return nil, nil
}

func (d *pzDB) CreateProject(_ context.Context, p *models.Project) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	p.ID = "proj-" + p.Name
	c := *p
	d.projects[p.ID] = &c

	return nil
}

func (d *pzDB) UpdateProject(_ context.Context, p *models.Project) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	c := *p
	d.projects[p.ID] = &c

	return nil
}

func (d *pzDB) DeleteProject(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.projects, id)
	delete(d.members, id)

	return nil
}

func (d *pzDB) ListProjects(_ context.Context, userID string) ([]*models.Project, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.Project

	for id, m := range d.members {
		if _, ok := m[userID]; ok && d.projects[id] != nil {
			out = append(out, d.projects[id])
		}
	}

	return out, nil
}

func (d *pzDB) ListAllProjects(context.Context) ([]*models.Project, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]*models.Project, 0, len(d.projects))
	for _, p := range d.projects {
		out = append(out, p)
	}

	return out, nil
}

func (d *pzDB) CountRepositoriesByProject(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (d *pzDB) AddProjectMember(_ context.Context, projectID, userID, role string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.members[projectID] == nil {
		d.members[projectID] = map[string]string{}
	}

	d.members[projectID][userID] = role

	return nil
}

func (d *pzDB) ListProjectGrantsByUser(_ context.Context, userID string) ([]*models.ProjectGrant, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.ProjectGrant

	for id, m := range d.members {
		if role, ok := m[userID]; ok {
			out = append(out, &models.ProjectGrant{ProjectID: id, Role: role})
		}
	}

	return out, nil
}

func (d *pzDB) member(projectID, userID string) *models.ProjectMember {
	role, ok := d.members[projectID][userID]
	if !ok {
		return nil
	}

	return &models.ProjectMember{ID: projectID + ":" + userID, ProjectID: projectID, UserID: userID, Username: d.users[userID].Username, Role: role}
}

func (d *pzDB) GetProjectMember(_ context.Context, projectID, userID string) (*models.ProjectMember, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.member(projectID, userID), nil
}

func (d *pzDB) ListProjectMembers(_ context.Context, projectID string) ([]*models.ProjectMember, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.ProjectMember
	for uid := range d.members[projectID] {
		out = append(out, d.member(projectID, uid))
	}

	return out, nil
}

// ChangeProjectMember mirrors PostgresDB.ChangeProjectMember: guard first,
// then the recorded-owner and last-owner invariants, all under one lock.
func (d *pzDB) ChangeProjectMember(_ context.Context, projectID, userID, newRole string, guard database.ProjectRoleGuard) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	p := d.projects[projectID]
	if p == nil {
		return database.ErrProjectNotFound
	}

	current := d.members[projectID][userID]

	if guard != nil {
		if err := guard(current); err != nil {
			return err
		}
	}

	if current != "" && current != newRole {
		if p.OwnerID != nil && *p.OwnerID == userID {
			return database.ErrProjectOwnerLocked
		}

		if current == models.ProjectRoleOwner {
			owners := 0

			for _, r := range d.members[projectID] {
				if r == models.ProjectRoleOwner {
					owners++
				}
			}

			if owners <= 1 {
				return database.ErrLastProjectOwner
			}
		}
	}

	switch newRole {
	case "":
		delete(d.members[projectID], userID)
	default:
		if d.members[projectID] == nil {
			d.members[projectID] = map[string]string{}
		}

		d.members[projectID][userID] = newRole
	}

	return nil
}

func (d *pzDB) ListProjectGroups(context.Context, string) ([]*models.ProjectGroup, error) {
	return nil, nil
}

func (d *pzDB) GetProjectGroup(_ context.Context, projectID, groupID string) (*models.ProjectGroup, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	role, ok := d.pgroups[projectID][groupID]
	if !ok {
		return nil, nil
	}

	return &models.ProjectGroup{ProjectID: projectID, GroupID: groupID, GroupName: d.groups[groupID].Name, Role: role}, nil
}

func (d *pzDB) ChangeProjectGroup(_ context.Context, projectID, groupID, newRole string, _ *string, guard database.ProjectRoleGuard) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	current := d.pgroups[projectID][groupID]

	if guard != nil {
		if err := guard(current); err != nil {
			return err
		}
	}

	if newRole == "" {
		delete(d.pgroups[projectID], groupID)
		return nil
	}

	if d.pgroups[projectID] == nil {
		d.pgroups[projectID] = map[string]string{}
	}

	d.pgroups[projectID][groupID] = newRole

	return nil
}

// pzFixture is the real middleware chain in front of the real handler.
type pzFixture struct {
	t      *testing.T
	db     *pzDB
	router http.Handler
	cookie map[string]*http.Cookie
}

func newPZFixture(t *testing.T) *pzFixture {
	t.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	str := func(s string) *string { return &s }
	db := &pzDB{
		admins: map[string]bool{"00000000-0000-4000-8000-0000000000a1": true},
		users: map[string]*models.User{
			"00000000-0000-4000-8000-0000000000a1": {ID: "00000000-0000-4000-8000-0000000000a1", Username: "admin"},
			"00000000-0000-4000-8000-0000000000a2": {ID: "00000000-0000-4000-8000-0000000000a2", Username: "olivia"},
			"00000000-0000-4000-8000-0000000000a3": {ID: "00000000-0000-4000-8000-0000000000a3", Username: "mark"},
			"00000000-0000-4000-8000-0000000000a4": {ID: "00000000-0000-4000-8000-0000000000a4", Username: "dana"},
			"00000000-0000-4000-8000-0000000000a5": {ID: "00000000-0000-4000-8000-0000000000a5", Username: "vic"},
			"00000000-0000-4000-8000-0000000000a6": {ID: "00000000-0000-4000-8000-0000000000a6", Username: "otto"},
			"00000000-0000-4000-8000-0000000000a7": {ID: "00000000-0000-4000-8000-0000000000a7", Username: "nina"},
		},
		projects: map[string]*models.Project{
			pzAcme: {ID: pzAcme, Name: "acme", OwnerID: str("00000000-0000-4000-8000-0000000000a2"), Slug: "acme"},
			pzBeta: {ID: pzBeta, Name: "beta", OwnerID: str("00000000-0000-4000-8000-0000000000a5"), Slug: "beta"},
		},
		members: map[string]map[string]string{
			pzAcme: {"00000000-0000-4000-8000-0000000000a2": "owner", "00000000-0000-4000-8000-0000000000a3": "maintainer", "00000000-0000-4000-8000-0000000000a4": "developer", "00000000-0000-4000-8000-0000000000a5": "viewer"},
			pzBeta: {"00000000-0000-4000-8000-0000000000a5": "owner"},
		},
		groups:   map[string]*models.Group{pzGroupEng: {ID: pzGroupEng, Name: "eng"}},
		pgroups:  map[string]map[string]string{},
		sessions: map[string]*models.Session{},
		tokens:   map[string]*models.Token{},
		settings: map[string]string{},
	}

	evaluator := auth.NewEvaluator(db, log, false)
	settings := auth.NewProjectSettings(db, false)

	router := chi.NewRouter()
	router.Use(middleware.RBACMiddleware(middleware.RBACConfig{ProjectSettings: settings}, db, evaluator, log))
	api.NewProjectsHandler(db, log, evaluator, settings).RegisterRoutes(router)

	f := &pzFixture{t: t, db: db, router: router, cookie: map[string]*http.Cookie{}}

	for id := range db.users {
		value, sess, err := auth.CreateSession(t.Context(), db, id, "", "")
		if err != nil {
			t.Fatal(err)
		}

		db.sessions[sess.TokenHash] = sess
		f.cookie[id] = &http.Cookie{Name: auth.SessionCookieName, Value: value}
	}

	return f
}

// do sends a request as the session of userID ("" = anonymous).
func (f *pzFixture) do(userID, method, path, body string) (int, map[string]any) {
	f.t.Helper()

	req := httptest.NewRequestWithContext(f.t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if c := f.cookie[userID]; c != nil {
		req.AddCookie(c)
	}

	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)

	var out map[string]any

	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	return rw.Code, out
}

func (f *pzFixture) deployToken(userID string) string {
	f.t.Helper()

	raw, tok, err := auth.CreateToken(f.t.Context(), f.db, auth.TokenKindDeploy, "ci", userID, nil, nil)
	if err != nil {
		f.t.Fatal(err)
	}

	now := time.Now()
	tok.LastUsedAt = &now
	f.db.tokens[tok.TokenHash] = tok

	return raw
}

func (f *pzFixture) expect(userID, method, path, body string, want int) map[string]any {
	f.t.Helper()

	got, out := f.do(userID, method, path, body)
	if got != want {
		f.t.Fatalf("%s %s %s as %q = %d, want %d (%v)", method, path, body, userID, got, want, out)
	}

	return out
}

func TestProjectAuthzE2E_OwnerManagesMembers(t *testing.T) {
	f := newPZFixture(t)

	// A non-admin owner adds a member by username.
	out := f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPost, "/api/v1/projects/acme/members", `{"username":"nina","role":"viewer"}`, http.StatusCreated)
	if out["username"] != "nina" || out["role"] != "viewer" {
		t.Fatalf("member = %v", out)
	}

	// The new viewer sees the project and its members, but cannot manage them.
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodGet, "/api/v1/projects/acme", "", http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodGet, "/api/v1/projects/acme/members", "", http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPost, "/api/v1/projects/acme/members", `{"username":"otto","role":"viewer"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPatch, "/api/v1/projects/acme", `{"description":"x"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodDelete, "/api/v1/projects/acme", "", http.StatusForbidden)

	// Duplicate add is a conflict; the owner changes and removes the role.
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPost, "/api/v1/projects/acme/members", `{"user_id":"00000000-0000-4000-8000-0000000000a7","role":"viewer"}`, http.StatusConflict)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPatch, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a7", `{"role":"maintainer"}`, http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodDelete, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a7", "", http.StatusNoContent)

	// Malformed member ids are plain misses, never database errors.
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPatch, "/api/v1/projects/acme/members/not-a-uuid", `{"role":"viewer"}`, http.StatusNotFound)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodDelete, "/api/v1/projects/acme/members/not-a-uuid", "", http.StatusNotFound)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPost, "/api/v1/projects/acme/members", `{"user_id":"not-a-uuid"}`, http.StatusBadRequest)

	// Removed: the project is invisible again.
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodGet, "/api/v1/projects/acme", "", http.StatusNotFound)
}

func TestProjectAuthzE2E_NonMemberAndUnknownLookAlike(t *testing.T) {
	f := newPZFixture(t)

	paths := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/projects/acme", ""},
		{http.MethodGet, "/api/v1/projects/acme/members", ""},
		{http.MethodGet, "/api/v1/projects/acme/repositories", ""},
		{http.MethodPost, "/api/v1/projects/acme/members", `{"username":"otto"}`},
		{http.MethodPatch, "/api/v1/projects/acme", `{"description":"x"}`},
		{http.MethodDelete, "/api/v1/projects/acme", ""},
	}

	for _, p := range paths {
		got := f.expect("00000000-0000-4000-8000-0000000000a6", p.method, p.path, p.body, http.StatusNotFound)
		ghost := f.expect("00000000-0000-4000-8000-0000000000a6", p.method, strings.Replace(p.path, "acme", "ghost", 1), p.body, http.StatusNotFound)

		if got["error"] != ghost["error"] {
			t.Errorf("%s %s: non-member body %v differs from unknown project body %v", p.method, p.path, got, ghost)
		}
	}

	// An owner of another project is a non-member here.
	f.expect("00000000-0000-4000-8000-0000000000a5", http.MethodGet, "/api/v1/projects/beta", "", http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodGet, "/api/v1/projects/beta", "", http.StatusNotFound)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPost, "/api/v1/projects/beta/members", `{"username":"otto"}`, http.StatusNotFound)
}

func TestProjectAuthzE2E_MaintainerEscalationGuards(t *testing.T) {
	f := newPZFixture(t)

	// A maintainer adds developers but cannot grant maintainer or owner.
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPost, "/api/v1/projects/acme/members", `{"username":"nina","role":"developer"}`, http.StatusCreated)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPost, "/api/v1/projects/acme/members", `{"username":"otto","role":"maintainer"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPost, "/api/v1/projects/acme/members", `{"username":"otto","role":"owner"}`, http.StatusForbidden)

	// Nor can they promote someone to owner, demote or remove an owner or
	// maintainer, or self-promote.
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPatch, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a4", `{"role":"owner"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPatch, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a3", `{"role":"owner"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPatch, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a2", `{"role":"viewer"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodDelete, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a2", "", http.StatusForbidden)

	// Groups: below maintainer only.
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPut, "/api/v1/projects/acme/groups/eng", `{"role":"maintainer"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPut, "/api/v1/projects/acme/groups/eng", `{"role":"developer"}`, http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPut, "/api/v1/projects/acme/groups/"+pzGroupEng, `{"role":"maintainer"}`, http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodPut, "/api/v1/projects/acme/groups/eng", `{"role":"viewer"}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a3", http.MethodDelete, "/api/v1/projects/acme/groups/eng", "", http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodDelete, "/api/v1/projects/acme/groups/eng", "", http.StatusNoContent)

	// The recorded owner and the last owner are protected from everyone.
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPatch, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a2", `{"role":"viewer"}`, http.StatusConflict)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodDelete, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a2", "", http.StatusConflict)
	f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodDelete, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a2", "", http.StatusConflict)

	// An owner may promote a second owner, who is then demotable.
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPatch, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a4", `{"role":"owner"}`, http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a4", http.MethodPatch, "/api/v1/projects/acme/members/00000000-0000-4000-8000-0000000000a4", `{"role":"viewer"}`, http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a4", http.MethodGet, "/api/v1/projects/acme", "", http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a4", http.MethodPatch, "/api/v1/projects/acme", `{"description":"x"}`, http.StatusForbidden)
}

func TestProjectAuthzE2E_PolicyAndUpdate(t *testing.T) {
	f := newPZFixture(t)

	// Owners edit display fields but never the policy.
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPatch, "/api/v1/projects/acme", `{"display_name":"Acme Inc"}`, http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodPatch, "/api/v1/projects/acme", `{"max_repositories":1000}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodPatch, "/api/v1/projects/acme", `{"max_repositories":1000}`, http.StatusOK)

	// PUT is no longer an update verb.
	if got, _ := f.do("00000000-0000-4000-8000-0000000000a1", http.MethodPut, "/api/v1/projects/acme", `{"display_name":"x"}`); got != http.StatusNotFound && got != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /projects/acme = %d, want 404 or 405", got)
	}

	// The project list shows the caller's role and hides foreign projects.
	out := f.expect("00000000-0000-4000-8000-0000000000a5", http.MethodGet, "/api/v1/projects", "", http.StatusOK)
	list, _ := out["projects"].([]any)

	roles := map[string]string{}

	for _, item := range list {
		p, _ := item.(map[string]any)
		name, _ := p["name"].(string)
		role, _ := p["my_role"].(string)
		roles[name] = role
	}

	if roles["acme"] != "viewer" || roles["beta"] != "owner" || len(roles) != 2 {
		t.Fatalf("viewer's projects = %v", roles)
	}

	if out["can_create"] != false {
		t.Fatalf("can_create = %v, want false", out["can_create"])
	}

	// all=true is for admins only.
	f.expect("00000000-0000-4000-8000-0000000000a5", http.MethodGet, "/api/v1/projects?all=true", "", http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodGet, "/api/v1/projects?all=true", "", http.StatusOK)

	// Admin oversight: any project, any member change, as an implicit owner.
	f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodGet, "/api/v1/projects/beta/members", "", http.StatusOK)
	f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodPost, "/api/v1/projects/beta/members", `{"username":"nina","role":"maintainer"}`, http.StatusCreated)
	f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodDelete, "/api/v1/projects/beta", "", http.StatusNoContent)
}

func TestProjectAuthzE2E_SelfCreate(t *testing.T) {
	f := newPZFixture(t)

	body := `{"name":"mine","display_name":"Mine"}`

	// Off by default: neither listing's can_create nor POST allows it.
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPost, "/api/v1/projects", body, http.StatusForbidden)

	if out := f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodGet, "/api/v1/projects", "", http.StatusOK); out["can_create"] != false {
		t.Fatalf("can_create = %v, want false", out["can_create"])
	}

	// Only admins flip the switch.
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPut, "/api/v1/admin/projects/settings", `{"self_create":true}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodPut, "/api/v1/admin/projects/settings", `{"self_create":true}`, http.StatusOK)

	if out := f.expect("00000000-0000-4000-8000-0000000000a1", http.MethodGet, "/api/v1/admin/projects/settings", "", http.StatusOK); out["self_create"] != true {
		t.Fatalf("settings = %v", out)
	}

	// On: any user creates a project and becomes its owner with default policy.
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPost, "/api/v1/projects", `{"name":"mine","max_repositories":9999}`, http.StatusForbidden)
	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPost, "/api/v1/projects", `{"name":"mine","owner":"otto"}`, http.StatusForbidden)

	created := f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPost, "/api/v1/projects", body, http.StatusCreated)
	if created["my_role"] != "owner" || created["owner_id"] != "00000000-0000-4000-8000-0000000000a7" {
		t.Fatalf("created = %v", created)
	}

	f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodPost, "/api/v1/projects/mine/members", `{"username":"otto","role":"viewer"}`, http.StatusCreated)
	f.expect("00000000-0000-4000-8000-0000000000a6", http.MethodGet, "/api/v1/projects/mine", "", http.StatusOK)

	if out := f.expect("00000000-0000-4000-8000-0000000000a7", http.MethodGet, "/api/v1/projects", "", http.StatusOK); out["can_create"] != true {
		t.Fatalf("can_create = %v, want true", out["can_create"])
	}

	// Deploy tokens never reach the control plane, even with the switch on.
	raw := f.deployToken("00000000-0000-4000-8000-0000000000a7")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"viatoken"}`))
	req.Header.Set("Authorization", "Bearer "+raw)

	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("deploy token POST /projects = %d, want 403", rw.Code)
	}

	// Anonymous callers are rejected before any project lookup.
	if got, _ := f.do("", http.MethodGet, "/api/v1/projects/acme", ""); got != http.StatusUnauthorized && got != http.StatusForbidden {
		t.Fatalf("anonymous = %d, want 401 or 403", got)
	}
}

func TestProjectAuthzE2E_UnknownRoutesStayAdminOnly(t *testing.T) {
	f := newPZFixture(t)

	for _, user := range []string{"00000000-0000-4000-8000-0000000000a2", "00000000-0000-4000-8000-0000000000a3", "00000000-0000-4000-8000-0000000000a5"} {
		f.expect(user, http.MethodGet, "/api/v1/projects/acme/secrets", "", http.StatusForbidden)
	}

	// Escaped names never match a project route.
	if got, _ := f.do("00000000-0000-4000-8000-0000000000a2", http.MethodGet, "/api/v1/projects/ac%6De", ""); got == http.StatusOK {
		t.Fatal("escaped project name passed the project route")
	}

	// Nobody but admins may touch the global admin API.
	f.expect("00000000-0000-4000-8000-0000000000a2", http.MethodGet, "/api/v1/admin/projects/settings", "", http.StatusForbidden)
}

// Activity never crosses projects: a member reads only the feed of a project
// they belong to, and that feed holds only that project's events.
func TestProjectAuthzE2E_ActivityIsProjectScoped(t *testing.T) {
	const (
		olivia = "00000000-0000-4000-8000-0000000000a2" // owner of acme
		dana   = "00000000-0000-4000-8000-0000000000a4" // developer in acme
		vic    = "00000000-0000-4000-8000-0000000000a5" // viewer in acme, owner of beta
		otto   = "00000000-0000-4000-8000-0000000000a6" // member of nothing
		admin  = "00000000-0000-4000-8000-0000000000a1"
	)

	f := newPZFixture(t)

	ip := "203.0.113.7"
	now := time.Now().UTC()

	event := func(i int, project, msg string) *models.AuditLog {
		return &models.AuditLog{
			ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
			Action:      "project.member_added",
			ProjectID:   &project,
			DetailsJSON: json.RawMessage(`{"message":"` + msg + `","actor":"olivia"}`),
			IPAddress:   &ip,
			CreatedAt:   now.Add(-time.Duration(i) * time.Minute),
		}
	}

	f.db.audits = []*models.AuditLog{
		event(1, pzAcme, "acme-1"), event(2, pzBeta, "beta-secret"), event(3, pzAcme, "acme-2"), event(4, pzBeta, "beta-secret-2"),
	}

	messages := func(out map[string]any) []string {
		var got []string

		for _, e := range out["data"].([]any) {
			entry := e.(map[string]any)
			got = append(got, entry["message"].(string))

			if _, leaked := entry["ip"]; leaked {
				t.Errorf("activity entry exposes the client address: %v", entry)
			}
		}

		return got
	}

	// Members of acme (any role) see acme's events and nothing of beta.
	for _, user := range []string{olivia, dana, vic, admin} {
		out := f.expect(user, http.MethodGet, "/api/v1/projects/acme/activity", "", http.StatusOK)
		if got := messages(out); !slices.Equal(got, []string{"acme-1", "acme-2"}) {
			t.Errorf("user %s: acme activity = %v, want [acme-1 acme-2]", user, got)
		}
	}

	// The viewer of acme who owns beta gets one feed per project.
	out := f.expect(vic, http.MethodGet, "/api/v1/projects/beta/activity", "", http.StatusOK)
	if got := messages(out); !slices.Equal(got, []string{"beta-secret", "beta-secret-2"}) {
		t.Errorf("beta activity = %v", got)
	}

	// Paging stays inside the project.
	out = f.expect(olivia, http.MethodGet, "/api/v1/projects/acme/activity?per_page=1", "", http.StatusOK)
	if out["has_more"] != true || out["next_cursor"] == "" {
		t.Fatalf("first page = %v, want has_more and a cursor", out)
	}

	// Not a member (acme-only members asking for beta, and a member of nothing): 404, the same as an unknown project.
	ghost := f.expect(otto, http.MethodGet, "/api/v1/projects/ghost/activity", "", http.StatusNotFound)

	for _, c := range []struct{ user, project string }{{olivia, "beta"}, {dana, "beta"}, {otto, "acme"}, {otto, "beta"}} {
		got := f.expect(c.user, http.MethodGet, "/api/v1/projects/"+c.project+"/activity", "", http.StatusNotFound)
		if got["error"] != ghost["error"] {
			t.Errorf("user %s on %s: body %v differs from unknown project body %v", c.user, c.project, got, ghost)
		}
	}

	// A malformed cursor is a client error, anonymous callers are not members.
	f.expect(olivia, http.MethodGet, "/api/v1/projects/acme/activity?cursor=%21%21", "", http.StatusBadRequest)
	f.expect("", http.MethodGet, "/api/v1/projects/acme/activity", "", http.StatusUnauthorized)

	// Deploy tokens carry no project role: refused like on every other project read.
	statusOf := func(path string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+f.deployToken(olivia))

		rw := httptest.NewRecorder()
		f.router.ServeHTTP(rw, req)

		return rw.Code
	}

	if got, want := statusOf("/api/v1/projects/acme/activity"), statusOf("/api/v1/projects/acme/members"); got != want || got < 400 {
		t.Errorf("deploy token activity = %d, members = %d; want the same refusal", got, want)
	}
}
