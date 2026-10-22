// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TestProjectsRouteMatrix checks every project route against every kind of
// caller: unknown projects and non-members get 404, members below the route's
// role 403, everybody else succeeds.
func TestProjectsRouteMatrix(t *testing.T) {
	routes := []struct {
		name   string
		method string
		path   string
		body   string
		min    string // lowest role allowed
	}{
		{"get project", http.MethodGet, "/api/v1/projects/acme", "", models.ProjectRoleViewer},
		{"list repositories", http.MethodGet, "/api/v1/projects/acme/repositories", "", models.ProjectRoleViewer},
		{"list members", http.MethodGet, "/api/v1/projects/acme/members", "", models.ProjectRoleViewer},
		{"list groups", http.MethodGet, "/api/v1/projects/acme/groups", "", models.ProjectRoleViewer},
		{"update project", http.MethodPatch, "/api/v1/projects/acme", `{"display_name":"x"}`, models.ProjectRoleOwner},
		{"delete project", http.MethodDelete, "/api/v1/projects/acme", "", models.ProjectRoleOwner},
		{"add member", http.MethodPost, "/api/v1/projects/acme/members", `{"username":"newbie","role":"viewer"}`, models.ProjectRoleMaintainer},
		{"change member role", http.MethodPatch, "/api/v1/projects/acme/members/" + uView1, `{"role":"developer"}`, models.ProjectRoleMaintainer},
		{"remove member", http.MethodDelete, "/api/v1/projects/acme/members/" + uView1, "", models.ProjectRoleMaintainer},
		{"attach group", http.MethodPut, "/api/v1/projects/acme/groups/" + testGroupName, `{"role":"viewer"}`, models.ProjectRoleMaintainer},
		{"detach group", http.MethodDelete, "/api/v1/projects/acme/groups/" + testGroupName, "", models.ProjectRoleMaintainer},
	}

	callers := []struct {
		user string
		role string // "" = not a member
	}{
		{uNewbie, ""},
		{uView1, models.ProjectRoleViewer},
		{uDev1, models.ProjectRoleDeveloper},
		{uMaint1, models.ProjectRoleMaintainer},
		{uOwner2, models.ProjectRoleOwner},
		{uAdmin1, models.ProjectRoleOwner}, // global admin: implicit owner
	}

	newDB := func() *projectsMockDB {
		db := newProjectsMock()
		db.projectGroup[testGroupID] = &models.ProjectGroup{ProjectID: testAcmeID, GroupID: testGroupID, GroupName: testGroupName, Role: models.ProjectRoleViewer}

		return db
	}

	for _, rt := range routes {
		for _, c := range callers {
			t.Run(rt.name+"/"+testUsernames[c.user], func(t *testing.T) {
				want := http.StatusNotFound

				switch {
				case c.role == "":
				case models.ProjectRoleRank(c.role) >= models.ProjectRoleRank(rt.min):
					want = 0 // any 2xx
				default:
					want = http.StatusForbidden
				}

				code, body := projectJSON(t, newProjectsRouter(t, newDB()), c.user, rt.method, rt.path, rt.body)
				if want == 0 && (code < 200 || code > 299) || want != 0 && code != want {
					t.Fatalf("status = %d, want %d (0 = 2xx); body = %v", code, want, body)
				}
			})
		}

		t.Run(rt.name+"/unknown project", func(t *testing.T) {
			path := strings.Replace(rt.path, "/projects/acme", "/projects/ghost", 1)

			for _, user := range []string{uAdmin1, uOwner1, uNewbie} {
				if code, body := projectJSON(t, newProjectsRouter(t, newDB()), user, rt.method, path, rt.body); code != http.StatusNotFound {
					t.Errorf("%s: status = %d, want 404; body = %v", user, code, body)
				}
			}
		})

		t.Run(rt.name+"/anonymous", func(t *testing.T) {
			if code, _ := projectJSONAs(t, newProjectsRouter(t, newDB()), nil, rt.method, rt.path, rt.body); code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", code)
			}

			deploy := deployPrincipal(uOwner1)
			if code, _ := projectJSONAs(t, newProjectsRouter(t, newDB()), deploy, rt.method, rt.path, rt.body); code != http.StatusNotFound {
				t.Errorf("deploy token: status = %d, want 404", code)
			}
		})
	}
}

// A role granted through a group counts like a membership.
func TestProjectsGroupGrantedRole(t *testing.T) {
	tests := []struct {
		role   string
		method string
		target string
		body   string
		want   int
	}{
		{models.ProjectRoleViewer, http.MethodGet, "/api/v1/projects/acme", "", http.StatusOK},
		{models.ProjectRoleViewer, http.MethodPost, "/api/v1/projects/acme/members", `{"username":"newbie"}`, http.StatusForbidden},
		{models.ProjectRoleMaintainer, http.MethodPost, "/api/v1/projects/acme/members", `{"username":"newbie"}`, http.StatusCreated},
		{models.ProjectRoleMaintainer, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.role+" "+tt.method+" "+tt.target, func(t *testing.T) {
			db := newProjectsMock()
			db.users[uGroup] = &models.User{ID: uGroup, Username: testUsernames[uGroup]}
			db.userGroups[uGroup] = []string{testGroupID}
			db.projectGroup[testGroupID] = &models.ProjectGroup{ProjectID: testAcmeID, GroupID: testGroupID, GroupName: testGroupName, Role: tt.role}

			code, body := projectJSON(t, newProjectsRouter(t, db), uGroup, tt.method, tt.target, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}
		})
	}
}

func TestProjectsAddMemberByUsername(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"by username", `{"username":"newbie","role":"developer"}`, http.StatusCreated},
		{"by username, default role", `{"username":"newbie"}`, http.StatusCreated},
		{"by user_id", `{"user_id":"0f000000-0000-4000-8000-000000000041","role":"developer"}`, http.StatusCreated},
		{"both", `{"user_id":"0f000000-0000-4000-8000-000000000041","username":"newbie","role":"developer"}`, http.StatusBadRequest},
		{"neither", `{"role":"developer"}`, http.StatusBadRequest},
		{"unknown username", `{"username":"ghost","role":"developer"}`, http.StatusNotFound},
		{"username of an existing member", `{"username":"dev-1","role":"viewer"}`, http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()

			code, body := projectJSON(t, newProjectsRouter(t, db), uOwner1, http.MethodPost, "/api/v1/projects/acme/members", tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.want != http.StatusCreated {
				if db.members[uNewbie] != nil {
					t.Error("member added despite the error")
				}

				return
			}

			if body["user_id"] != uNewbie || body["username"] != "newbie" || body["project_id"] != testAcmeID {
				t.Errorf("response = %v", body)
			}
		})
	}
}

func TestProjectsMalformedMemberID(t *testing.T) {
	const members = "/api/v1/projects/acme/members"

	tests := []struct {
		name   string
		user   string
		method string
		target string
		body   string
		want   int
	}{
		{"PATCH non-UUID path id", uOwner1, http.MethodPatch, members + "/dev-1", `{"role":"viewer"}`, http.StatusNotFound},
		{"DELETE non-UUID path id", uOwner1, http.MethodDelete, members + "/dev-1", "", http.StatusNotFound},
		{"DELETE injection-looking path id", uOwner1, http.MethodDelete, members + "/%27%3B--", "", http.StatusNotFound},
		{"DELETE truncated UUID", uOwner1, http.MethodDelete, members + "/" + uDev1[:35], "", http.StatusNotFound},
		{"admin PATCH non-UUID path id", uAdmin1, http.MethodPatch, members + "/ghost", `{"role":"viewer"}`, http.StatusNotFound},
		{"POST non-UUID user_id", uOwner1, http.MethodPost, members, `{"user_id":"newbie","role":"viewer"}`, http.StatusBadRequest},
		{"POST non-UUID user_id with username", uOwner1, http.MethodPost, members, `{"user_id":"not-a-uuid","username":"newbie"}`, http.StatusBadRequest},
		{"admin POST non-UUID user_id", uAdmin1, http.MethodPost, members, `{"user_id":"1","role":"viewer"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()

			code, body := projectJSON(t, newProjectsRouter(t, db), tt.user, tt.method, tt.target, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if db.memberLookups != 0 {
				t.Errorf("%d user/member lookups before rejecting the malformed id, want 0", db.memberLookups)
			}

			if len(db.removed) != 0 || len(db.roleUpdates) != 0 || db.members[uNewbie] != nil {
				t.Errorf("state changed: removed %v, roles %v", db.removed, db.roleUpdates)
			}
		})
	}
}

func TestProjectsListMembersCarriesUsername(t *testing.T) {
	code, body := projectJSON(t, newProjectsRouter(t, newProjectsMock()), uView1, http.MethodGet, "/api/v1/projects/acme/members", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d; body = %v", code, body)
	}

	list, _ := body["members"].([]any)
	if len(list) != 6 {
		t.Fatalf("members = %v, want 6", list)
	}

	for _, item := range list {
		m, _ := item.(map[string]any)
		id, _ := m["user_id"].(string)
		if m["username"] == "" || m["username"] != testUsernames[id] {
			t.Errorf("member = %v, want username filled", m)
		}
	}
}

func TestProjectsRecordedOwnerAndLastOwner(t *testing.T) {
	tests := []struct {
		name   string
		user   string
		method string
		target string
		body   string
		setup  func(db *projectsMockDB)
		want   int
	}{
		{"recorded owner cannot be demoted by an owner", uOwner2, http.MethodPatch, "/members/" + uOwner1, `{"role":"developer"}`, nil, http.StatusConflict},
		{"recorded owner cannot be removed by an owner", uOwner2, http.MethodDelete, "/members/" + uOwner1, "", nil, http.StatusConflict},
		{"recorded owner cannot be demoted by an admin", uAdmin1, http.MethodPatch, "/members/" + uOwner1, `{"role":"developer"}`, nil, http.StatusConflict},
		{"recorded owner keeps the owner role", uOwner2, http.MethodPatch, "/members/" + uOwner1, `{"role":"owner"}`, nil, http.StatusOK},
		{
			"last owner cannot be removed", uOwner1, http.MethodDelete, "/members/" + uOwner2, "",
			func(db *projectsMockDB) { delete(db.members, uOwner1) }, http.StatusConflict,
		},
		{
			"last owner cannot be demoted", uOwner1, http.MethodPatch, "/members/" + uOwner2, `{"role":"maintainer"}`,
			func(db *projectsMockDB) { delete(db.members, uOwner1) }, http.StatusConflict,
		},
		{"a second owner can be demoted", uOwner1, http.MethodPatch, "/members/" + uOwner2, `{"role":"maintainer"}`, nil, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			if tt.setup != nil {
				tt.setup(db)
			}

			code, body := projectJSON(t, newProjectsRouter(t, db), tt.user, tt.method, "/api/v1/projects/acme"+tt.target, tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.want == http.StatusConflict && body["code"] != "conflict" {
				t.Errorf("code = %v, want conflict", body["code"])
			}

			if tt.want == http.StatusConflict && (len(db.removed) != 0 || len(db.roleUpdates) != 0) {
				t.Errorf("changed despite the conflict: removed %v, roles %v", db.removed, db.roleUpdates)
			}
		})
	}
}

func repoFixtures(n int) []*models.Repository {
	repos := make([]*models.Repository, 0, n)
	now := time.Now()

	for i := range n {
		repos = append(repos, &models.Repository{
			ID: "repo-" + strconv.Itoa(i), Name: "acme-" + strconv.Itoa(i), Format: "raw", RepoType: "hosted",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	return repos
}

func TestProjectsListRepositories(t *testing.T) {
	tests := []struct {
		name      string
		user      string
		query     string
		repos     int
		wantNames []string
		wantTotal float64
		wantPage  float64
		wantLimit float64
	}{
		{"first page default size", uView1, "", 3, []string{"acme-0", "acme-1", "acme-2"}, 3, 1, 25},
		{"page 1 of 2", uDev1, "?page=1&limit=2", 3, []string{"acme-0", "acme-1"}, 3, 1, 2},
		{"page 2 of 2", uDev1, "?page=2&limit=2", 3, []string{"acme-2"}, 3, 2, 2},
		{"page past the end", uOwner1, "?page=9&limit=2", 3, []string{}, 3, 9, 2},
		{"limit is capped", uAdmin1, "?limit=1000", 3, []string{"acme-0", "acme-1", "acme-2"}, 3, 1, 100},
		{"invalid page falls back", uView1, "?page=abc&limit=2", 3, []string{"acme-0", "acme-1"}, 3, 1, 2},
		{"empty project", uView1, "", 0, []string{}, 0, 1, 25},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			db.projectRepos = repoFixtures(tt.repos)

			code, body := projectJSON(t, newProjectsRouter(t, db), tt.user, http.MethodGet, "/api/v1/projects/acme/repositories"+tt.query, "")
			if code != http.StatusOK {
				t.Fatalf("status = %d; body = %v", code, body)
			}

			if body["total"] != tt.wantTotal || body["page"] != tt.wantPage || body["limit"] != tt.wantLimit {
				t.Errorf("total/page/limit = %v/%v/%v, want %v/%v/%v", body["total"], body["page"], body["limit"], tt.wantTotal, tt.wantPage, tt.wantLimit)
			}

			list, ok := body["repositories"].([]any)
			if !ok || len(list) != len(tt.wantNames) {
				t.Fatalf("repositories = %v, want %v", body["repositories"], tt.wantNames)
			}

			for i, item := range list {
				r, _ := item.(map[string]any)
				if r["name"] != tt.wantNames[i] || r["project"] != "acme" {
					t.Errorf("repository %d = %v, want %s in project acme", i, r, tt.wantNames[i])
				}
			}
		})
	}
}

func TestProjectsGetCarriesRoleAndCount(t *testing.T) {
	tests := []struct {
		user     string
		wantRole string
	}{
		{uView1, "viewer"},
		{uDev1, "developer"},
		{uMaint1, "maintainer"},
		{uOwner2, "owner"},
		{uAdmin1, "owner"},
	}

	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			db := newProjectsMock()
			db.projectRepos = repoFixtures(3)

			code, body := projectJSON(t, newProjectsRouter(t, db), tt.user, http.MethodGet, "/api/v1/projects/acme", "")
			if code != http.StatusOK {
				t.Fatalf("status = %d; body = %v", code, body)
			}

			if body["my_role"] != tt.wantRole || body["repository_count"] != float64(3) || body["is_system"] != false {
				t.Errorf("response = %v, want role %s, 3 repositories, not system", body, tt.wantRole)
			}
		})
	}
}

func TestProjectsSystemProject(t *testing.T) {
	newDB := func() *projectsMockDB {
		db := newProjectsMock()
		db.projects["unassigned"] = &models.Project{ID: models.SystemProjectID, Name: "unassigned", Slug: "unassigned", AllowedRepoTypes: []string{models.ProjectRepoTypeHosted}}

		return db
	}

	t.Run("delete is a conflict even for an admin", func(t *testing.T) {
		db := newDB()

		code, body := projectJSON(t, newProjectsRouter(t, db), uAdmin1, http.MethodDelete, "/api/v1/projects/unassigned", "")
		if code != http.StatusConflict {
			t.Fatalf("status = %d; body = %v", code, body)
		}

		if len(db.deleted) != 0 {
			t.Errorf("deleted = %v", db.deleted)
		}
	})

	t.Run("get is flagged is_system", func(t *testing.T) {
		code, body := projectJSON(t, newProjectsRouter(t, newDB()), uAdmin1, http.MethodGet, "/api/v1/projects/unassigned", "")
		if code != http.StatusOK || body["is_system"] != true || body["my_role"] != "owner" {
			t.Errorf("response = %d %v, want is_system true, owner", code, body)
		}
	})

	t.Run("a member of the system project gets no role", func(t *testing.T) {
		db := newDB()
		db.members["dev-1-sys"] = &models.ProjectMember{ID: "m-sys", ProjectID: models.SystemProjectID, UserID: uDev1, Role: models.ProjectRoleOwner}

		if code, body := projectJSON(t, newProjectsRouter(t, db), uDev1, http.MethodDelete, "/api/v1/projects/unassigned", ""); code != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body = %v", code, body)
		}
	})
}

func TestAuthMeProjects(t *testing.T) {
	scoped := scopedPrincipal(uDev1)
	plain := testPrincipal

	tests := []struct {
		name      string
		principal *auth.Principal
		want      map[string]string // project name -> role
	}{
		{"member lists name and role", plain(uDev1), map[string]string{"acme": "developer"}},
		{"recorded owner", plain(uOwner1), map[string]string{"acme": "owner"}},
		{"admin who is not a member has no implicit entry", plain(uAdmin1), map[string]string{}},
		{"user without projects", plain(uNewbie), map[string]string{}},
		{"scoped token has none", scoped, map[string]string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			for _, id := range []string{uDev1, uOwner1} {
				db.userProjects[id] = []*models.Project{db.projects["acme"]}
			}

			log := newFixTestLogger(t)
			router := chi.NewRouter()
			NewAuthHandler(db, log, auth.NewEvaluator(db, log, false), nil).RegisterAuthed(router)

			code, body := projectJSONAs(t, router, tt.principal, http.MethodGet, "/api/v1/auth/me", "")
			if code != http.StatusOK {
				t.Fatalf("status = %d; body = %v", code, body)
			}

			list, ok := body["projects"].([]any)
			if !ok {
				t.Fatalf("projects = %v, want a JSON array", body["projects"])
			}

			got := map[string]string{}

			for _, item := range list {
				p, _ := item.(map[string]any)
				name, _ := p["name"].(string)
				role, _ := p["role"].(string)
				got[name] = role
			}

			if len(got) != len(tt.want) || len(list) != len(tt.want) {
				t.Fatalf("projects = %v, want %v", got, tt.want)
			}

			for name, role := range tt.want {
				if got[name] != role {
					t.Errorf("project %s role = %q, want %q", name, got[name], role)
				}
			}
		})
	}
}
