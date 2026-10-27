// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

func TestProjectsSelfCreate(t *testing.T) {
	scoped := scopedPrincipal(uPlain1)
	deploy := deployPrincipal(uPlain1)

	tests := []struct {
		name       string
		stored     string // stored projects.self_create ("" = unset)
		envDefault bool
		principal  *auth.Principal
		body       string
		want       int
	}{
		{"off by default: plain user", "", false, nil, `{"name":"Mine"}`, http.StatusForbidden},
		{"off: stored false", "false", false, nil, `{"name":"Mine"}`, http.StatusForbidden},
		{"stored false overrides env default on", "false", true, nil, `{"name":"Mine"}`, http.StatusForbidden},
		{"malformed stored value fails closed", "banana", true, nil, `{"name":"Mine"}`, http.StatusForbidden},
		{"on: stored true", "true", false, nil, `{"name":"Mine"}`, http.StatusCreated},
		{"on: env default", "", true, nil, `{"name":"Mine"}`, http.StatusCreated},
		{
			"on: display name and description allowed",
			"true",
			false,
			nil,
			`{"name":"Mine","display_name":"Mine","description":"d"}`,
			http.StatusCreated,
		},
		{"on: scoped token denied", "true", false, scoped, `{"name":"Mine"}`, http.StatusForbidden},
		{"on: deploy token denied", "true", false, deploy, `{"name":"Mine"}`, http.StatusForbidden},
		{"on: owner field 403", "true", false, nil, `{"name":"Mine","owner":"newbie"}`, http.StatusForbidden},
		{"on: allowed_formats 403", "true", false, nil, `{"name":"Mine","allowed_formats":["raw"]}`, http.StatusForbidden},
		{"on: allowed_repo_types 403", "true", false, nil, `{"name":"Mine","allowed_repo_types":["hosted","proxy"]}`, http.StatusForbidden},
		{"on: repo_name_prefix 403", "true", false, nil, `{"name":"Mine","repo_name_prefix":""}`, http.StatusForbidden},
		{"on: allow_public_repos 403", "true", false, nil, `{"name":"Mine","allow_public_repos":true}`, http.StatusForbidden},
		{"on: max_repositories 403", "true", false, nil, `{"name":"Mine","max_repositories":1000}`, http.StatusForbidden},
		{"on: token_max_ttl_days 403", "true", false, nil, `{"name":"Mine","token_max_ttl_days":0}`, http.StatusForbidden},
		{"on: validation still applies", "true", false, nil, `{"name":"no spaces"}`, http.StatusBadRequest},
		{"on: duplicate name", "true", false, nil, `{"name":"acme"}`, http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			if tt.stored != "" {
				db.settings[auth.SettingProjectsSelfCreate] = tt.stored
			}

			router := newProjectsRouterDefault(t, db, tt.envDefault)

			principal := tt.principal
			if principal == nil {
				principal = testPrincipal(uPlain1)
			}

			code, body := projectJSONAs(t, router, principal, http.MethodPost, "/api/v1/projects", tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.want != http.StatusCreated {
				if db.createdProject != nil && tt.want != http.StatusConflict {
					t.Errorf("project created despite the error: %+v", db.createdProject)
				}

				return
			}

			if body["my_role"] != models.ProjectRoleOwner || body["repository_count"] != float64(0) || body["is_system"] != false {
				t.Errorf("response = %v, want owner / 0 repositories / not system", body)
			}
		})
	}
}

func TestProjectsSelfCreateOwnerAndDefaults(t *testing.T) {
	db := newProjectsMock()
	db.settings[auth.SettingProjectsSelfCreate] = "true"
	router := newProjectsRouter(t, db)

	code, body := projectJSON(t, router, uPlain1, http.MethodPost, "/api/v1/projects", `{"name":"Mine_Proj"}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d; body = %v", code, body)
	}

	p := db.createdProject
	if p == nil {
		t.Fatal("project not created")
	}

	if p.OwnerID == nil || *p.OwnerID != uPlain1 {
		t.Errorf("owner = %v, want plain-1", p.OwnerID)
	}

	if m := db.members[uPlain1]; m == nil || m.Role != models.ProjectRoleOwner || m.ProjectID != p.ID {
		t.Errorf("creator membership = %+v, want owner of %s", m, p.ID)
	}

	if p.RepoNamePrefix != "mine-proj-" || p.MaxRepositories != 50 || p.TokenMaxTTLDays != 365 || p.AllowPublicRepos ||
		p.AllowedFormats != nil || !slices.Equal(p.AllowedRepoTypes, []string{models.ProjectRepoTypeHosted}) {
		t.Errorf("policy = %+v, want the defaults", p)
	}

	// The new owner can use the project right away.
	if code, body := projectJSON(
		t,
		router,
		uPlain1,
		http.MethodGet,
		"/api/v1/projects/Mine_Proj",
		"",
	); code != http.StatusOK ||
		body["my_role"] != "owner" {
		t.Errorf("GET new project = %d %v, want 200 owner", code, body)
	}
}

func TestProjectsAdminCreateForOwner(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      int
		wantOwner string
	}{
		{"owner by username", `{"name":"Fresh","owner":"newbie"}`, http.StatusCreated, uNewbie},
		{"no owner: the admin owns it", `{"name":"Fresh"}`, http.StatusCreated, uAdmin1},
		{"owner with policy", `{"name":"Fresh","owner":"newbie","max_repositories":5}`, http.StatusCreated, uNewbie},
		{"unknown owner", `{"name":"Fresh","owner":"ghost"}`, http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, uAdmin1, http.MethodPost, "/api/v1/projects", tt.body)
			if code != tt.want {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.want, body)
			}

			if tt.want != http.StatusCreated {
				if db.createdProject != nil {
					t.Error("project created despite the error")
				}

				return
			}

			if got := db.createdProject.OwnerID; got == nil || *got != tt.wantOwner {
				t.Errorf("owner_id = %v, want %s", got, tt.wantOwner)
			}

			if m := db.members[tt.wantOwner]; m == nil || m.Role != models.ProjectRoleOwner {
				t.Errorf("owner membership = %+v, want owner", m)
			}

			if tt.wantOwner != uAdmin1 && db.members[uAdmin1] != nil {
				t.Error("the admin became a member of a project it created for somebody else")
			}

			// The response is the caller's view: an admin acts as owner.
			if body["my_role"] != "owner" || body["owner_id"] != tt.wantOwner {
				t.Errorf("response = %v", body)
			}
		})
	}
}

func TestProjectsListCanCreate(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		user   string
		want   bool
	}{
		{"off: plain user", "", uPlain1, false},
		{"on: plain user", "true", uPlain1, true},
		{"on: member", "true", uDev1, true},
		{"off: admin", "", uAdmin1, true},
		{"stored false: admin", "false", uAdmin1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			if tt.stored != "" {
				db.settings[auth.SettingProjectsSelfCreate] = tt.stored
			}

			code, body := projectJSON(t, newProjectsRouter(t, db), tt.user, http.MethodGet, "/api/v1/projects", "")
			if code != http.StatusOK || body["can_create"] != tt.want {
				t.Fatalf("status/can_create = %d/%v, want 200/%v", code, body["can_create"], tt.want)
			}
		})
	}
}

func TestProjectsListScopedToken(t *testing.T) {
	db := newProjectsMock()
	db.userProjects[uDev1] = []*models.Project{db.projects["acme"]}
	router := newProjectsRouter(t, db)

	scoped := scopedPrincipal(uDev1)

	code, body := projectJSONAs(t, router, scoped, http.MethodGet, "/api/v1/projects", "")
	if list, _ := body["projects"].([]any); code != http.StatusOK || list == nil || len(list) != 0 {
		t.Errorf("scoped token list = %d %v, want 200 and an empty list", code, body)
	}

	if code, _ := projectJSONAs(t, router, scoped, http.MethodGet, "/api/v1/projects/acme", ""); code != http.StatusNotFound {
		t.Errorf("scoped token GET project = %d, want 404", code)
	}
}

func TestProjectsAdminSettings(t *testing.T) {
	const target = "/api/v1/admin/projects/settings"

	t.Run("GET reflects stored value over env default", func(t *testing.T) {
		tests := []struct {
			stored string
			env    bool
			want   bool
		}{
			{"", false, false},
			{"", true, true},
			{"true", false, true},
			{"false", true, false},
		}

		for _, tt := range tests {
			db := newProjectsMock()
			if tt.stored != "" {
				db.settings[auth.SettingProjectsSelfCreate] = tt.stored
			}

			code, body := projectJSON(t, newProjectsRouterDefault(t, db, tt.env), uAdmin1, http.MethodGet, target, "")
			if code != http.StatusOK || body["self_create"] != tt.want {
				t.Errorf("stored %q env %v: %d %v, want self_create %v", tt.stored, tt.env, code, body, tt.want)
			}
		}
	})

	t.Run("non-admins are denied", func(t *testing.T) {
		for _, user := range []string{uPlain1, uOwner1, uMaint1, uDev1} {
			db := newProjectsMock()
			router := newProjectsRouter(t, db)

			if code, body := projectJSON(t, router, user, http.MethodGet, target, ""); code != http.StatusForbidden {
				t.Errorf("%s GET = %d %v, want 403", user, code, body)
			}

			if code, body := projectJSON(t, router, user, http.MethodPut, target, `{"self_create":true}`); code != http.StatusForbidden {
				t.Errorf("%s PUT = %d %v, want 403", user, code, body)
			}

			if _, set := db.settings[auth.SettingProjectsSelfCreate]; set {
				t.Errorf("%s changed the setting", user)
			}
		}
	})

	t.Run("anonymous is 401", func(t *testing.T) {
		router := newProjectsRouter(t, newProjectsMock())

		for _, method := range []string{http.MethodGet, http.MethodPut} {
			if code, _ := projectJSONAs(t, router, nil, method, target, `{"self_create":true}`); code != http.StatusUnauthorized {
				t.Errorf("%s = %d, want 401", method, code)
			}
		}
	})
}

func TestProjectsAdminSettingsPut(t *testing.T) {
	const target = "/api/v1/admin/projects/settings"

	t.Run("PUT persists and takes effect", func(t *testing.T) {
		db := newProjectsMock()
		router := newProjectsRouter(t, db)

		if code, _ := projectJSON(
			t,
			router,
			uPlain1,
			http.MethodPost,
			"/api/v1/projects",
			`{"name":"Mine"}`,
		); code != http.StatusForbidden {
			t.Fatalf("create before = %d, want 403", code)
		}

		code, body := projectJSON(t, router, uAdmin1, http.MethodPut, target, `{"self_create":true}`)
		if code != http.StatusOK || body["self_create"] != true {
			t.Fatalf("PUT = %d %v", code, body)
		}

		if got := db.settings[auth.SettingProjectsSelfCreate]; got != "true" {
			t.Errorf("stored value = %q, want true", got)
		}

		if code, body := projectJSON(
			t,
			router,
			uPlain1,
			http.MethodPost,
			"/api/v1/projects",
			`{"name":"Mine"}`,
		); code != http.StatusCreated {
			t.Fatalf("create after = %d %v, want 201", code, body)
		}

		if code, _ := projectJSON(t, router, uAdmin1, http.MethodPut, target, `{"self_create":false}`); code != http.StatusOK {
			t.Fatalf("PUT off = %d", code)
		}

		if got := db.settings[auth.SettingProjectsSelfCreate]; got != "false" {
			t.Errorf("stored value = %q, want false", got)
		}

		if code, _ := projectJSON(
			t,
			router,
			uPlain1,
			http.MethodPost,
			"/api/v1/projects",
			`{"name":"Other"}`,
		); code != http.StatusForbidden {
			t.Errorf("create after off = %d, want 403", code)
		}
	})

	t.Run("PUT with an invalid body", func(t *testing.T) {
		db := newProjectsMock()

		if code, _ := projectJSON(t, newProjectsRouter(t, db), uAdmin1, http.MethodPut, target, `nope`); code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", code)
		}

		if len(db.settings) != 0 {
			t.Errorf("settings written: %v", db.settings)
		}
	})
}
