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
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

func setupSSORouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	baseURL := "http://localhost:8080"
	cfg := &config.Config{}
	h := api.NewAdminSSOHandler(db, log, eval, baseURL, cfg)

	router := chi.NewRouter()
	h.RegisterPublic(router)
	h.RegisterAuthed(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "sso-admin-1",
		Username:  "ssoadmin",
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

func setupSystemRouter(t *testing.T) chi.Router {
	t.Helper()

	handler := api.NewSystemInfoHandler("v1.0.0", "abc1234", "2026-01-01T00:00:00Z", nil, nil, "")
	router := chi.NewRouter()
	handler.RegisterPublic(router)

	return router
}

func withPrincipalSSO(req *http.Request, p *auth.Principal) *http.Request {
	return req.WithContext(middleware.WithPrincipal(req.Context(), p))
}

// SSO Handler Tests

func TestSSO_PublicList_Empty(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupSSORouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/providers", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	providers, ok := resp["providers"].([]any)
	if !ok {
		t.Fatalf("expected providers array, got %T", resp["providers"])
	}

	if len(providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(providers))
	}
}

func TestSSO_PublicList_WithProviders(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()
	now := time.Now()

	provider := &models.SSOProvider{
		ID:          "test-sso-1",
		Slug:        "test-oidc",
		Kind:        "oidc",
		DisplayName: "Test OIDC",
		Enabled:     true,
		OIDCConfig:  json.RawMessage(`{"issuer":"https://example.com","client_id":"test","client_secret":"secret"}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.CreateSSOProvider(ctx, provider); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	router, _ := setupSSORouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/providers", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	providers, ok := resp["providers"].([]any)
	if !ok {
		t.Fatalf("expected providers array, got %T", resp["providers"])
	}

	if len(providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(providers))
	}

	p := providers[0].(map[string]any)
	if p["slug"] != "test-oidc" {
		t.Errorf("expected slug=test-oidc, got %v", p["slug"])
	}

	if p["kind"] != "oidc" {
		t.Errorf("expected kind=oidc, got %v", p["kind"])
	}

	if p["display_name"] != "Test OIDC" {
		t.Errorf("expected display_name=Test OIDC, got %v", p["display_name"])
	}

	// Verify sensitive fields are not included
	if _, hasOIDCConfig := p["oidc_config"]; hasOIDCConfig {
		t.Error("public list must not include oidc_config")
	}

	if _, hasID := p["id"]; hasID {
		t.Error("public list must not include id")
	}
}

func TestSSO_AdminList(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()
	now := time.Now()

	provider := &models.SSOProvider{
		ID:          "test-sso-2",
		Slug:        "test-saml",
		Kind:        "saml",
		DisplayName: "Test SAML",
		Enabled:     true,
		SAMLConfig:  json.RawMessage(`{"idp_metadata_url":"https://example.com/metadata"}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.CreateSSOProvider(ctx, provider); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	router, principal := setupSSORouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/sso-providers", nil)
	req = withPrincipalSSO(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	providers, ok := resp["providers"].([]any)
	if !ok {
		t.Fatalf("expected providers array, got %T", resp["providers"])
	}

	if len(providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(providers))
	}
}

func TestSSO_Create(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSSORouter(t, db)

	oidcCfg := map[string]string{
		"issuer":        "https://203.0.113.10",
		"client_id":     "test-client",
		"client_secret": "test-secret",
	}

	oidcCfgBytes, err := json.Marshal(oidcCfg)
	if err != nil {
		t.Fatalf("marshal oidc_config: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"slug":         "new-oidc",
		"kind":         "oidc",
		"display_name": "New OIDC Provider",
		"oidc_config":  json.RawMessage(oidcCfgBytes),
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/sso-providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipalSSO(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["slug"] != "new-oidc" {
		t.Errorf("expected slug=new-oidc, got %v", resp["slug"])
	}

	if resp["kind"] != "oidc" {
		t.Errorf("expected kind=oidc, got %v", resp["kind"])
	}

	if resp["display_name"] != "New OIDC Provider" {
		t.Errorf("expected display_name=New OIDC Provider, got %v", resp["display_name"])
	}

	if resp["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", resp["enabled"])
	}
}

func TestSSO_Get(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()
	now := time.Now()

	provider := &models.SSOProvider{
		ID:          "test-sso-3",
		Slug:        "get-test",
		Kind:        "oidc",
		DisplayName: "Get Test Provider",
		Enabled:     true,
		OIDCConfig:  json.RawMessage(`{"issuer":"https://example.com","client_id":"test","client_secret":"secret"}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.CreateSSOProvider(ctx, provider); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	router, principal := setupSSORouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/sso-providers/get-test", nil)
	req = withPrincipalSSO(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["slug"] != "get-test" {
		t.Errorf("expected slug=get-test, got %v", resp["slug"])
	}
}

func TestSSO_Update(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()
	now := time.Now()

	provider := &models.SSOProvider{
		ID:          "test-sso-4",
		Slug:        "update-test",
		Kind:        "oidc",
		DisplayName: "Original Name",
		Enabled:     true,
		OIDCConfig:  json.RawMessage(`{"issuer":"https://example.com","client_id":"test","client_secret":"secret"}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.CreateSSOProvider(ctx, provider); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	router, principal := setupSSORouter(t, db)

	patch := map[string]any{
		"display_name": "Updated Name",
		"enabled":      false,
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/admin/sso-providers/update-test", bytes.NewReader(patchBytes))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipalSSO(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["display_name"] != "Updated Name" {
		t.Errorf("expected display_name=Updated Name, got %v", resp["display_name"])
	}

	if resp["enabled"] != false {
		t.Errorf("expected enabled=false, got %v", resp["enabled"])
	}
}

func TestSSO_Delete(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()
	now := time.Now()

	provider := &models.SSOProvider{
		ID:          "test-sso-5",
		Slug:        "delete-test",
		Kind:        "oidc",
		DisplayName: "Delete Test Provider",
		Enabled:     true,
		OIDCConfig:  json.RawMessage(`{"issuer":"https://example.com","client_id":"test","client_secret":"secret"}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.CreateSSOProvider(ctx, provider); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	router, principal := setupSSORouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/admin/sso-providers/delete-test", nil)
	req = withPrincipalSSO(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	// Verify it's deleted
	deleted, err := db.GetSSOProviderBySlug(ctx, "delete-test")
	if err != nil {
		t.Fatalf("GetSSOProviderBySlug: %v", err)
	}

	if deleted != nil {
		t.Error("expected provider to be deleted")
	}
}

func TestSSO_RequiresAdmin(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()
	now := time.Now()

	// Create non-admin user
	nonAdminUser := &models.User{
		ID:        "sso-nonadmin-1",
		Username:  "nonadmin",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, nonAdminUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	router, _ := setupSSORouter(t, db)

	nonAdminPrincipal := &auth.Principal{
		UserID:      &nonAdminUser.ID,
		Username:    nonAdminUser.Username,
		IsAnonymous: false,
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/sso-providers", nil)
	req = withPrincipalSSO(req, nonAdminPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["code"] != "admin_required" {
		t.Errorf("expected code=admin_required, got %v", resp["code"])
	}
}

// System Info Handler Tests

func TestSystemInfo_Get(t *testing.T) {
	router := setupSystemRouter(t)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/system/info", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["version"] != "v1.0.0" {
		t.Errorf("expected version=v1.0.0, got %v", resp["version"])
	}

	// The public endpoint is trimmed: version and status only.
	for _, field := range []string{"commit", "build_time", "go_version", "os", "arch"} {
		if _, ok := resp[field]; ok {
			t.Errorf("public endpoint must not expose %s", field)
		}
	}

	if resp["status"] == "" || resp["status"] == nil {
		t.Errorf("missing status")
	}
}

func TestSystemInfo_MethodNotAllowed(t *testing.T) {
	router := setupSystemRouter(t)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/system/info", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rw.Code)
	}
}
