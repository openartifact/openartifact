// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

func setupRepoRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewRepositoriesHandler(db, log, eval)

	router := chi.NewRouter()
	h.RegisterAuthed(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "repos-admin-1",
		Username:  "reposadmin",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, adminUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	uid := adminUser.ID
	if err := db.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: "981948bd-de8f-5c65-b90b-23d72cf37aab",
		PrincipalType:      "user",
		PrincipalID:        uid,
	}); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	principal := &auth.Principal{
		UserID:      &uid,
		Username:    adminUser.Username,
		IsAnonymous: false,
	}

	return router, principal
}

func withPrincipal(req *http.Request, p *auth.Principal) *http.Request {
	return req.WithContext(middleware.WithPrincipal(req.Context(), p))
}

func seedRepo(t *testing.T, db *MockDatabase, name, format string) *models.Repository {
	t.Helper()

	now := time.Now()

	repo := &models.Repository{
		Name:      name,
		Format:    format,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	return repo
}

func TestRepositoriesHandler_HandleList_Pagination(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	seedRepo(t, db, "repo-a", "rpm")
	seedRepo(t, db, "repo-b", "rpm")
	seedRepo(t, db, "repo-c", "deb")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/repositories?page=1&limit=2", nil)
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	totalVal, ok := resp["total"].(float64)
	if !ok {
		t.Fatalf("total not found or not a float64")
	}

	total := int(totalVal)
	if total != 3 {
		t.Errorf("expected total=3, got %d", total)
	}

	repos := resp["repositories"].([]any)
	if len(repos) != 2 {
		t.Errorf("expected 2 repos in page, got %d", len(repos))
	}
}

func TestRepositoriesHandler_HandleList_FormatFilter(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	seedRepo(t, db, "rpm-repo-1", "rpm")
	seedRepo(t, db, "rpm-repo-2", "rpm")
	seedRepo(t, db, "deb-repo-1", "deb")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/repositories?format=rpm&limit=100", nil)
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	repos := resp["repositories"].([]any)
	for _, r := range repos {
		row := r.(map[string]any)
		if row["format"] != "rpm" {
			t.Errorf("expected format=rpm, got %v", row["format"])
		}
	}

	if len(repos) != 2 {
		t.Errorf("expected 2 rpm repos, got %d", len(repos))
	}
}

func TestRepositoriesHandler_HandleGet_NotFound(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/repositories/nonexistent", nil)
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rw.Code)
	}
}

func TestRepositoriesHandler_HandleGet_Found(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	repo := seedRepo(t, db, "my-repo", "npm")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/repository/my-repo", nil)
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got["name"] != repo.Name {
		t.Errorf("expected name=%q, got %v", repo.Name, got["name"])
	}

	if got["format"] != repo.Format {
		t.Errorf("expected format=%q, got %v", repo.Format, got["format"])
	}
}

func TestRepositoriesHandler_HandleCreate_Success(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	body, _ := json.Marshal(map[string]any{
		"name":   "new-repo",
		"format": "rpm",
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/repositories", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got["name"] != "new-repo" {
		t.Errorf("expected name=new-repo, got %v", got["name"])
	}

	stored, err := db.GetRepositoryByName(context.Background(), "new-repo")
	if err != nil || stored == nil {
		t.Errorf("repo not stored in db: err=%v, stored=%v", err, stored)
	}
}

func TestRepositoriesHandler_HandleCreate_Validation(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	body, _ := json.Marshal(map[string]any{
		"name":   "bad name with spaces",
		"format": "rpm",
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/repositories", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestRepositoriesHandler_HandleCreate_NotAdmin(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupRepoRouter(t, db)

	nonAdminID := "plain-user-1"
	nonAdmin := &auth.Principal{
		UserID:      &nonAdminID,
		Username:    "plainuser",
		IsAnonymous: false,
	}

	body, _ := json.Marshal(map[string]any{
		"name":   "should-fail",
		"format": "rpm",
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/repositories", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, nonAdmin)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestRepositoriesHandler_HandleUpdate(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	seedRepo(t, db, "update-me", "maven")

	desc := "updated"
	body, _ := json.Marshal(map[string]any{
		"description": desc,
	})
	req := httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/repository/update-me", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got["description"] != desc {
		t.Errorf("expected description=%q, got %v", desc, got["description"])
	}
}

func TestRepositoriesHandler_HandleDelete(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupRepoRouter(t, db)

	seedRepo(t, db, "delete-me", "deb")

	req := httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/repository/delete-me?confirm=delete-me", nil)
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}

	deleted, err := db.GetRepositoryByName(context.Background(), "delete-me")
	if err != nil {
		t.Fatalf("GetRepositoryByName: %v", err)
	}

	if deleted != nil {
		t.Errorf("expected repo to be deleted, but still found")
	}
}
