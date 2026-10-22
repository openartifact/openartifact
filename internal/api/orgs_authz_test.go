// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

type orgAuthzMockDB struct {
	repoCreateMockDB

	orgs       map[string]*models.Organization // by name
	createdOrg *models.Organization
	updated    *models.Organization
	repo       *models.Repository
	updRepo    *models.Repository
	deleteErr  error
}

func (db *orgAuthzMockDB) DeleteOrganization(context.Context, string) error { return db.deleteErr }

func (db *orgAuthzMockDB) GetOrganizationByName(_ context.Context, name string) (*models.Organization, error) {
	return db.orgs[name], nil
}

func (db *orgAuthzMockDB) GetOrganization(_ context.Context, id string) (*models.Organization, error) {
	for _, o := range db.orgs {
		if o.ID == id {
			return o, nil
		}
	}

	return nil, nil
}

func (db *orgAuthzMockDB) CreateOrganization(_ context.Context, org *models.Organization) error {
	for _, o := range db.orgs {
		if o.Name == org.Name || o.Slug == org.Slug {
			return &pgconn.PgError{Code: "23505"}
		}
	}

	org.ID = "new-org"
	db.createdOrg = org

	return nil
}

func (db *orgAuthzMockDB) AddOrgMember(context.Context, string, string, string) error { return nil }

func (db *orgAuthzMockDB) UpdateOrganization(_ context.Context, org *models.Organization) error {
	db.updated = org
	return nil
}

func (db *orgAuthzMockDB) GetOrgMember(_ context.Context, orgID, userID string) (*models.OrgMember, error) {
	return &models.OrgMember{OrgID: orgID, UserID: userID, Role: orgRoleOwner}, nil
}

func (db *orgAuthzMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if db.repo != nil && db.repo.Name == name {
		cp := *db.repo
		return &cp, nil
	}

	return nil, nil
}

func (db *orgAuthzMockDB) UpdateRepository(_ context.Context, repo *models.Repository) error {
	db.updRepo = repo
	return nil
}

var _ database.Database = (*orgAuthzMockDB)(nil)

func newOrgAuthzMock() *orgAuthzMockDB {
	db := &orgAuthzMockDB{orgs: map[string]*models.Organization{
		"acme": {ID: "org-acme", Name: "acme", Slug: "acme", MemberRole: models.OrgAccessWrite},
	}}
	db.targets = adminTargets()

	return db
}

func orgJSON(t *testing.T, router http.Handler, method, target, body string) (int, map[string]any) {
	t.Helper()

	userID := "admin-1"
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "admin"}))

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	out := map[string]any{}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	return rw.Code, out
}

func TestOrgsMemberRole(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		wantRole   string // member_role in the response / stored
	}{
		{"create defaults to read", http.MethodPost, "/api/v1/orgs", `{"name":"x"}`, http.StatusCreated, "read"},
		{"create with write", http.MethodPost, "/api/v1/orgs", `{"name":"x","member_role":"write"}`, http.StatusCreated, "write"},
		{"create rejects owner", http.MethodPost, "/api/v1/orgs", `{"name":"x","member_role":"owner"}`, http.StatusBadRequest, ""},
		{"update to admin", http.MethodPut, "/api/v1/orgs/acme", `{"member_role":"admin"}`, http.StatusOK, "admin"},
		{"update rejects junk", http.MethodPut, "/api/v1/orgs/acme", `{"member_role":"manage"}`, http.StatusBadRequest, ""},
		{"update without member_role keeps it", http.MethodPut, "/api/v1/orgs/acme", `{"description":"d"}`, http.StatusOK, "write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOrgAuthzMock()
			router := chi.NewRouter()
			NewOrgsHandler(db, newFixTestLogger(t)).RegisterRoutes(router)

			code, body := orgJSON(t, router, tt.method, tt.target, tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.wantStatus, body)
			}

			if tt.wantRole == "" {
				return
			}

			if body["member_role"] != tt.wantRole {
				t.Errorf("response member_role = %v, want %s", body["member_role"], tt.wantRole)
			}
		})
	}
}

func TestRepositoriesOrgAssignment(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		wantOrgID  string // org_id stored; "" when nothing is written
		wantOrg    string // org in the response
	}{
		{"create defaults to system org", http.MethodPost, "/api/v1/repositories", `{"name":"r","format":"raw"}`, http.StatusCreated, models.SystemOrgID, ""},
		{"create in named org", http.MethodPost, "/api/v1/repositories", `{"name":"r","format":"raw","org":"acme"}`, http.StatusCreated, "org-acme", "acme"},
		{"create in unknown org", http.MethodPost, "/api/v1/repositories", `{"name":"r","format":"raw","org":"ghost"}`, http.StatusNotFound, "", ""},
		{"move to named org", http.MethodPatch, "/api/v1/repository/old", `{"org":"acme"}`, http.StatusOK, "org-acme", "acme"},
		{"move to unknown org", http.MethodPatch, "/api/v1/repository/old", `{"org":"ghost"}`, http.StatusNotFound, "", ""},
		{"patch without org keeps it", http.MethodPatch, "/api/v1/repository/old", `{"description":"d"}`, http.StatusOK, models.SystemOrgID, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOrgAuthzMock()
			sys := models.SystemOrgID
			db.repo = &models.Repository{ID: "repo-old", Name: "old", Format: "raw", RepoType: "hosted", OrgID: &sys}

			log := newFixTestLogger(t)
			h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))
			router := chi.NewRouter()
			h.RegisterAuthed(router)

			code, body := orgJSON(t, router, tt.method, tt.target, tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.wantStatus, body)
			}

			if tt.wantOrgID == "" {
				return
			}

			stored := db.created
			if tt.method == http.MethodPatch {
				stored = db.updRepo
			}

			if stored == nil || stored.OrgID == nil || *stored.OrgID != tt.wantOrgID {
				t.Fatalf("stored org_id = %v, want %s", stored, tt.wantOrgID)
			}

			if got, _ := body["org"].(string); got != tt.wantOrg {
				t.Errorf("response org = %q, want %q", got, tt.wantOrg)
			}
		})
	}
}

func TestOrgsCreateSlug(t *testing.T) {
	long := strings.Repeat("a", 65)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantSlug   string
	}{
		{"slug derived from name", `{"name":"My_Org"}`, http.StatusCreated, "my-org"},
		{"duplicate name", `{"name":"acme"}`, http.StatusConflict, ""},
		{"slug-only collision (case)", `{"name":"Acme"}`, http.StatusConflict, ""},
		{"bad name", `{"name":"no spaces"}`, http.StatusBadRequest, ""},
		{"name without letters or digits", `{"name":"---"}`, http.StatusBadRequest, ""},
		{"slug too long", `{"name":"` + long + `"}`, http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newOrgAuthzMock()
			router := chi.NewRouter()
			NewOrgsHandler(db, newFixTestLogger(t)).RegisterRoutes(router)

			code, body := orgJSON(t, router, http.MethodPost, "/api/v1/orgs", tt.body)
			if code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.wantStatus, body)
			}

			if tt.wantSlug != "" && body["slug"] != tt.wantSlug {
				t.Errorf("slug = %v, want %s", body["slug"], tt.wantSlug)
			}
		})
	}
}

func TestOrgsDeleteStillOwnsRepositories(t *testing.T) {
	db := newOrgAuthzMock()
	owner := "admin-1"
	db.orgs["acme"].OwnerID = &owner
	db.deleteErr = &pgconn.PgError{Code: "23503"}

	router := chi.NewRouter()
	NewOrgsHandler(db, newFixTestLogger(t)).RegisterRoutes(router)

	code, body := orgJSON(t, router, http.MethodDelete, "/api/v1/orgs/acme", "")
	if code != http.StatusConflict || body["code"] != "conflict" {
		t.Fatalf("status/body = %d/%v, want 409/conflict", code, body)
	}

	if msg, _ := body["error"].(string); !strings.Contains(msg, "in the trash") {
		t.Errorf("message %q does not mention repositories in the trash", msg)
	}
}
