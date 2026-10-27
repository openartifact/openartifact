// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// captureAudit installs an audit logger over db (whose InsertAuditLog records
// the rows) as the process default. The returned function stops the logger,
// which drains its queue, and returns every recorded row in order.
func captureAudit(t *testing.T, db *projectsMockDB) func() []*models.AuditLog {
	t.Helper()

	l := audit.NewLogger(db, newFixTestLogger(t), nil, 64)
	audit.SetDefault(l)

	t.Cleanup(func() {
		audit.SetDefault(nil)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = l.Shutdown(ctx)
	})

	return func() []*models.AuditLog {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := l.Shutdown(ctx); err != nil {
			t.Fatalf("audit shutdown: %v", err)
		}

		db.mu.Lock()
		defer db.mu.Unlock()

		return slices.Clone(db.audits)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}

	return *s
}

func TestProjectsAuditEvents(t *testing.T) {
	enable := func(db *projectsMockDB) { db.settings[auth.SettingProjectsSelfCreate] = "true" }
	attach := func(role string) func(db *projectsMockDB) {
		return func(db *projectsMockDB) {
			db.projectGroup[testGroupID] = &models.ProjectGroup{
				ProjectID: testAcmeID,
				GroupID:   testGroupID,
				GroupName: testGroupName,
				Role:      role,
			}
		}
	}

	const (
		memberType = "project_member"
		groupType  = "project_group"
	)

	tests := []struct {
		name   string
		setup  func(db *projectsMockDB)
		user   string
		method string
		target string
		body   string
		status int
		// expected rows: action, project_id ("<nil>" for none), resource_type, resource_id
		want [][4]string
	}{
		{
			"created", nil, uAdmin1, http.MethodPost, "/api/v1/projects", `{"name":"Fresh"}`, http.StatusCreated,
			[][4]string{{"project.created", "new-project", "project", "new-project"}},
		},
		{
			"created by a self-service user", enable, uPlain1, http.MethodPost, "/api/v1/projects", `{"name":"Fresh"}`, http.StatusCreated,
			[][4]string{{"project.created", "new-project", "project", "new-project"}},
		},
		{
			"rejected create emits nothing", nil, uPlain1, http.MethodPost, "/api/v1/projects", `{"name":"Fresh"}`, http.StatusForbidden,
			nil,
		},
		{
			"updated", nil, uOwner1, http.MethodPatch, "/api/v1/projects/acme", `{"display_name":"New"}`, http.StatusOK,
			[][4]string{{"project.updated", testAcmeID, "project", testAcmeID}},
		},
		{
			"update without a change emits nothing",
			nil,
			uOwner1,
			http.MethodPatch,
			"/api/v1/projects/acme",
			`{"display_name":"Acme"}`,
			http.StatusOK,
			nil,
		},
		{
			"policy changed", nil, uAdmin1, http.MethodPatch, "/api/v1/projects/acme", `{"max_repositories":99}`, http.StatusOK,
			[][4]string{{"project.policy_changed", testAcmeID, "project", testAcmeID}},
		},
		{
			"updated and policy changed",
			nil,
			uAdmin1,
			http.MethodPatch,
			"/api/v1/projects/acme",
			`{"description":"x","token_max_ttl_days":1}`,
			http.StatusOK,
			[][4]string{
				{"project.updated", testAcmeID, "project", testAcmeID},
				{"project.policy_changed", testAcmeID, "project", testAcmeID},
			},
		},
		{
			"deleted has no project id", nil, uOwner1, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusNoContent,
			[][4]string{{"project.deleted", "<nil>", "project", testAcmeID}},
		},
		{
			"failed delete emits nothing", nil, uMaint1, http.MethodDelete, "/api/v1/projects/acme", "", http.StatusForbidden,
			nil,
		},
		{
			"member added",
			nil,
			uOwner1,
			http.MethodPost,
			"/api/v1/projects/acme/members",
			`{"username":"newbie","role":"developer"}`,
			http.StatusCreated,
			[][4]string{{"project.member_added", testAcmeID, memberType, uNewbie}},
		},
		{
			"vetoed member add emits nothing",
			nil,
			uMaint1,
			http.MethodPost,
			"/api/v1/projects/acme/members",
			`{"user_id":"0f000000-0000-4000-8000-000000000041","role":"owner"}`,
			http.StatusForbidden,
			nil,
		},
		{
			"member role changed",
			nil,
			uOwner1,
			http.MethodPatch,
			"/api/v1/projects/acme/members/" + uDev1,
			`{"role":"maintainer"}`,
			http.StatusOK,
			[][4]string{{"project.member_role_changed", testAcmeID, memberType, uDev1}},
		},
		{
			"same member role emits nothing",
			nil,
			uOwner1,
			http.MethodPatch,
			"/api/v1/projects/acme/members/" + uDev1,
			`{"role":"developer"}`,
			http.StatusOK,
			nil,
		},
		{
			"member removed", nil, uMaint1, http.MethodDelete, "/api/v1/projects/acme/members/" + uView1, "", http.StatusNoContent,
			[][4]string{{"project.member_removed", testAcmeID, memberType, uView1}},
		},
		{
			"locked owner removal emits nothing",
			nil,
			uOwner2,
			http.MethodDelete,
			"/api/v1/projects/acme/members/" + uOwner1,
			"",
			http.StatusConflict,
			nil,
		},
		{
			"group added",
			nil,
			uMaint1,
			http.MethodPut,
			"/api/v1/projects/acme/groups/" + testGroupName,
			`{"role":"developer"}`,
			http.StatusOK,
			[][4]string{{"project.group_added", testAcmeID, groupType, testGroupID}},
		},
		{
			"group role changed",
			attach(models.ProjectRoleDeveloper),
			uMaint1,
			http.MethodPut,
			"/api/v1/projects/acme/groups/" + testGroupID,
			`{"role":"viewer"}`,
			http.StatusOK,
			[][4]string{{"project.group_role_changed", testAcmeID, groupType, testGroupID}},
		},
		{
			"same group role emits nothing",
			attach(models.ProjectRoleDeveloper),
			uMaint1,
			http.MethodPut,
			"/api/v1/projects/acme/groups/" + testGroupID,
			`{"role":"developer"}`,
			http.StatusOK,
			nil,
		},
		{
			"group removed",
			attach(models.ProjectRoleDeveloper),
			uMaint1,
			http.MethodDelete,
			"/api/v1/projects/acme/groups/" + testGroupName,
			"",
			http.StatusNoContent,
			[][4]string{{"project.group_removed", testAcmeID, groupType, testGroupID}},
		},
		{
			"vetoed group removal emits nothing",
			attach(models.ProjectRoleMaintainer),
			uMaint1,
			http.MethodDelete,
			"/api/v1/projects/acme/groups/" + testGroupID,
			"",
			http.StatusForbidden,
			nil,
		},
		{
			"settings changed", nil, uAdmin1, http.MethodPut, "/api/v1/admin/projects/settings", `{"self_create":true}`, http.StatusOK,
			[][4]string{{"system.setting_changed", "<nil>", "system_setting", auth.SettingProjectsSelfCreate}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newProjectsMock()
			if tt.setup != nil {
				tt.setup(db)
			}

			flush := captureAudit(t, db)
			router := newProjectsRouter(t, db)

			code, body := projectJSON(t, router, tt.user, tt.method, tt.target, tt.body)
			if code != tt.status {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.status, body)
			}

			logs := flush()

			got := make([][4]string, 0, len(logs))
			for _, l := range logs {
				got = append(got, [4]string{l.Action, deref(l.ProjectID), deref(l.ResourceType), deref(l.ResourceID)})
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("audit rows = %v, want %v", got, tt.want)
			}

			for _, l := range logs {
				if l.ActorID == nil || *l.ActorID != tt.user || deref(l.ActorType) != "user" {
					t.Errorf("%s actor = %s/%s, want user %s", l.Action, deref(l.ActorID), deref(l.ActorType), tt.user)
				}

				if !strings.Contains(string(l.DetailsJSON), `"actor":"`+testUsernames[tt.user]+`"`) {
					t.Errorf("%s details = %s, want the actor name", l.Action, l.DetailsJSON)
				}
			}
		})
	}
}

// Without an installed audit logger the handlers still work.
func TestProjectsWithoutAuditLogger(t *testing.T) {
	audit.SetDefault(nil)

	db := newProjectsMock()

	if code, body := projectJSON(
		t,
		newProjectsRouter(t, db),
		uAdmin1,
		http.MethodPost,
		"/api/v1/projects",
		`{"name":"Fresh"}`,
	); code != http.StatusCreated {
		t.Fatalf("status = %d; body = %v", code, body)
	}
}
