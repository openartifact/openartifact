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

func setupStorageRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewAdminStorageHandler(db, log, eval)
	router := chi.NewRouter()
	h.Register(router)

	// Create admin user
	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "storage-admin-1",
		Username:  "storageadmin",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, adminUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := db.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: "981948bd-de8f-5c65-b90b-23d72cf37aab",
		PrincipalType:      "user",
		PrincipalID:        adminUser.ID,
	}); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	principal := &auth.Principal{
		UserID:      &adminUser.ID,
		Username:    adminUser.Username,
		IsAnonymous: false,
	}

	return router, principal
}

func withStoragePrincipal(req *http.Request, p *auth.Principal) *http.Request {
	return req.WithContext(middleware.WithPrincipal(req.Context(), p))
}

func seedStorageBackend(t *testing.T, db *MockDatabase, name, kind string, config map[string]any) *models.StorageBackend {
	t.Helper()

	now := time.Now()

	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	backend := &models.StorageBackend{
		ID:        "backend-" + name,
		Name:      name,
		Kind:      kind,
		IsDefault: false,
		Config:    configBytes,
		Status:    "unknown",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.CreateStorageBackend(context.Background(), backend); err != nil {
		t.Fatalf("CreateStorageBackend(%s): %v", name, err)
	}

	return backend
}

// CRUD Tests

func TestAdminStorage_ListEmpty(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/storage", nil)
	req = withStoragePrincipal(req, principal)
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
	if total != 0 {
		t.Errorf("expected total=0, got %d", total)
	}

	items := resp["items"].([]any)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestAdminStorage_Create(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	body := map[string]any{
		"name":   "test-local",
		"kind":   "filesystem",
		"config": map[string]any{"path": "/tmp/test"},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/storage", bytes.NewReader(bodyBytes))
	req = withStoragePrincipal(req, principal)
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["name"] != "test-local" {
		t.Errorf("expected name=test-local, got %v", resp["name"])
	}

	if resp["kind"] != "filesystem" {
		t.Errorf("expected kind=filesystem, got %v", resp["kind"])
	}
}

func TestAdminStorage_CreateInvalidKind(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	body := map[string]any{
		"name": "test",
		"kind": "invalid",
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/storage", bytes.NewReader(bodyBytes))
	req = withStoragePrincipal(req, principal)
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminStorage_CreateMissingName(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	body := map[string]any{
		"kind": "filesystem",
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/storage", bytes.NewReader(bodyBytes))
	req = withStoragePrincipal(req, principal)
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminStorage_Get(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	seedStorageBackend(t, db, "my-backend", "filesystem", map[string]any{"path": "/tmp/test"})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/storage/my-backend", nil)
	req = withStoragePrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["name"] != "my-backend" {
		t.Errorf("expected name=my-backend, got %v", resp["name"])
	}

	if resp["kind"] != "filesystem" {
		t.Errorf("expected kind=filesystem, got %v", resp["kind"])
	}
}

func TestAdminStorage_GetNotFound(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/storage/nonexistent", nil)
	req = withStoragePrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminStorage_Update(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	seedStorageBackend(t, db, "my-backend", "filesystem", map[string]any{"path": "/tmp/test"})

	patch := map[string]any{
		"config": map[string]any{"path": "/new/path"},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/admin/storage/my-backend", bytes.NewReader(patchBytes))
	req = withStoragePrincipal(req, principal)
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["name"] != "my-backend" {
		t.Errorf("expected name=my-backend, got %v", resp["name"])
	}
}

func TestAdminStorage_Delete(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	seedStorageBackend(t, db, "my-backend", "filesystem", map[string]any{"path": "/tmp/test"})

	req := httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/admin/storage/my-backend", nil)
	req = withStoragePrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}

	// Verify it's deleted
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/storage/my-backend", nil)
	req = withStoragePrincipal(req, principal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rw.Code)
	}
}

func TestAdminStorage_DeleteDefault(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	// Create a default backend
	now := time.Now()
	configBytes, _ := json.Marshal(map[string]any{"path": "/tmp/test"})

	backend := &models.StorageBackend{
		ID:        "default-backend",
		Name:      "default",
		Kind:      "filesystem",
		IsDefault: true,
		Config:    configBytes,
		Status:    "unknown",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.CreateStorageBackend(context.Background(), backend); err != nil {
		t.Fatalf("CreateStorageBackend: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/admin/storage/default", nil)
	req = withStoragePrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminStorage_Test(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupStorageRouter(t, db)

	// Create a filesystem backend with a valid temp directory
	seedStorageBackend(t, db, "test-backend", "filesystem", map[string]any{"path": "/tmp"})

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/storage/test-backend/test", nil)
	req = withStoragePrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if ok, exists := resp["ok"]; !exists || !ok.(bool) {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}
}

// Auth Tests

func TestAdminStorage_RequiresAdmin(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupStorageRouter(t, db)

	// Create a non-admin user
	ctx := context.Background()
	now := time.Now()

	regularUser := &models.User{
		ID:        "regular-user-1",
		Username:  "regularuser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, regularUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	nonAdminPrincipal := &auth.Principal{
		UserID:      &regularUser.ID,
		Username:    regularUser.Username,
		IsAnonymous: false,
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/storage", nil)
	req = withStoragePrincipal(req, nonAdminPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rw.Code, rw.Body.String())
	}
}
