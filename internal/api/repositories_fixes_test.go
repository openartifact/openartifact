// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/openartifact/openartifact/pkg/logger"
)

func newFixTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	return log
}

func TestValidFormatsMatchDatabaseConstraint(t *testing.T) {
	if !validFormats["go"] {
		t.Error(`validFormats must accept "go" (chk_repo_format)`)
	}

	if validFormats["gomod"] {
		t.Error(`validFormats must not accept "gomod": the database constraint rejects it`)
	}

	msg := validFormatList()
	for f := range validFormats {
		if !strings.Contains(msg, f) {
			t.Errorf("validFormatList() = %q does not mention %q", msg, f)
		}
	}
}

type repoCreateMockDB struct {
	database.BaseMockDB

	createErr error
	targets   []*models.PermissionTarget
	created   *models.Repository
}

func (db *repoCreateMockDB) CreateRepositoryWithMembers(_ context.Context, repo *models.Repository, _ []string) error {
	db.created = repo
	return db.createErr
}

func (db *repoCreateMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return db.targets, nil
}

func adminRequest(db *repoCreateMockDB, method, target, body string) *http.Request {
	userID := "admin-1"
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))

	return req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "admin"}))
}

func adminTargets() []*models.PermissionTarget {
	return []*models.PermissionTarget{{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: []string{"manage", "read", "write"}}}
}

func TestRepositoriesCreateMapsUniqueViolationTo409(t *testing.T) {
	log := newFixTestLogger(t)
	db := &repoCreateMockDB{
		createErr: &pgconn.PgError{Code: "23505"},
		targets:   adminTargets(),
	}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"dup","format":"go"}`))

	if rw.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rw.Code, rw.Body.String())
	}

	db.createErr = errors.New("boom")
	rw = httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"other","format":"go"}`))

	if rw.Code != http.StatusInternalServerError {
		t.Fatalf("generic db error status = %d, want 500", rw.Code)
	}

	if strings.Contains(rw.Body.String(), "boom") {
		t.Error("internal error detail leaked to client")
	}
}

func TestRepositoriesCreatePersistsPublicFlag(t *testing.T) {
	log := newFixTestLogger(t)
	db := &repoCreateMockDB{targets: adminTargets()}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"pub","format":"go","public":true}`))

	if rw.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}

	if db.created == nil || !db.created.Public {
		t.Error("public flag was not passed to the database layer")
	}
}

func TestRepositoriesListClampsHugePage(t *testing.T) {
	log := newFixTestLogger(t)
	db := &repositoriesListMockDB{
		repos:   []*models.Repository{{Name: "a", Format: "rpm"}},
		targets: adminTargets(),
	}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))
	userID := "u"

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories?page=9223372036854775807&limit=100", nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "u"}))
	rw := httptest.NewRecorder()

	h.handleList(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}
}

type orgOwnerMockDB struct {
	database.BaseMockDB

	org     *models.Organization
	members map[string]*models.OrgMember
	removed []string
}

func (db *orgOwnerMockDB) GetOrganizationByName(_ context.Context, _ string) (*models.Organization, error) {
	return db.org, nil
}

func (db *orgOwnerMockDB) GetOrgMember(_ context.Context, _, userID string) (*models.OrgMember, error) {
	return db.members[userID], nil
}

func (db *orgOwnerMockDB) ListOrgMembers(_ context.Context, _ string) ([]*models.OrgMember, error) {
	out := make([]*models.OrgMember, 0, len(db.members))
	for _, m := range db.members {
		out = append(out, m)
	}

	return out, nil
}

func (db *orgOwnerMockDB) RemoveOrgMember(_ context.Context, _, userID string) error {
	db.removed = append(db.removed, userID)
	return nil
}

func orgRemoveRequest(caller, target string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/orgs/acme/members/"+target, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &caller, Username: caller}))

	return req
}

func TestOrgsRemoveMemberProtectsOwners(t *testing.T) {
	log := newFixTestLogger(t)
	recordedOwner := "owner-1"

	newDB := func() *orgOwnerMockDB {
		return &orgOwnerMockDB{
			org: &models.Organization{ID: "org-1", Name: "acme", OwnerID: &recordedOwner},
			members: map[string]*models.OrgMember{
				"owner-1": {OrgID: "org-1", UserID: "owner-1", Role: orgRoleOwner},
				"admin-1": {OrgID: "org-1", UserID: "admin-1", Role: orgRoleAdmin},
				"user-1":  {OrgID: "org-1", UserID: "user-1", Role: orgRoleMember},
			},
		}
	}

	do := func(db *orgOwnerMockDB, caller, target string) int {
		router := chi.NewRouter()
		NewOrgsHandler(db, log).RegisterRoutes(router)

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, orgRemoveRequest(caller, target))

		return rw.Code
	}

	t.Run("admin cannot remove the owner", func(t *testing.T) {
		db := newDB()
		if code := do(db, "admin-1", "owner-1"); code != http.StatusConflict && code != http.StatusForbidden {
			t.Fatalf("status = %d, want 409/403", code)
		}

		if len(db.removed) != 0 {
			t.Error("owner was removed")
		}
	})

	t.Run("owner cannot remove themselves as the recorded owner", func(t *testing.T) {
		db := newDB()
		if code := do(db, "owner-1", "owner-1"); code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", code)
		}
	})

	t.Run("admin can remove a plain member", func(t *testing.T) {
		db := newDB()
		if code := do(db, "admin-1", "user-1"); code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", code)
		}
	})

	t.Run("plain member cannot remove anyone", func(t *testing.T) {
		db := newDB()
		if code := do(db, "user-1", "admin-1"); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", code)
		}
	})
}

func TestStorageBackendResponseRedactsSecrets(t *testing.T) {
	b := &models.StorageBackend{
		ID: "1", Name: "s3", Kind: "s3",
		Config: []byte(`{"bucket":"b","access_key":"AKIA","secret_key":"shh","secretKey":"shh2"}`),
	}

	resp := storageBackendToResponse(b)

	for _, k := range []string{"access_key", "secret_key", "secretKey"} {
		if _, ok := resp.Config[k]; ok {
			t.Errorf("secret %q returned by API", k)
		}
	}

	if resp.Config["has_secret_key"] != true || resp.Config["has_access_key"] != true {
		t.Errorf("has_* flags missing: %v", resp.Config)
	}

	if resp.Config["bucket"] != "b" {
		t.Error("non-secret config dropped")
	}
}

func TestPrepareStorageConfigKeepsOmittedSecrets(t *testing.T) {
	existing := []byte(`{"bucket":"b","access_key":"AK","secret_key":"SK"}`)

	out, err := prepareStorageConfig("s3", []byte(`{"bucket":"b2","access_key":"","secret_key":""}`), existing)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["bucket"] != "b2" {
		t.Errorf("bucket = %v, want b2", got["bucket"])
	}

	for _, k := range []string{"access_key", "secret_key"} {
		if s, _ := got[k].(string); s == "" {
			t.Errorf("stored %s lost on update with empty value: %s", k, out)
		}
	}

	if _, err := prepareStorageConfig("filesystem", []byte(`{"path":"../etc"}`), nil); err == nil {
		t.Error("path traversal accepted")
	}

	if _, err := prepareStorageConfig("garage", []byte(`{"bucket":"b"}`), nil); err == nil {
		t.Error("garage without endpoint accepted")
	}
}
