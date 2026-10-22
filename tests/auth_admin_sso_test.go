// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

func setupAdminSSORouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()

	ctx := context.Background()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	cfg := &config.Config{BaseURL: "http://localhost:8080"}
	h := api.NewAdminSSOHandler(db, log, eval, "http://localhost:8080", cfg)

	router := chi.NewRouter()
	h.RegisterAuthed(router)

	now := time.Now()

	adminUser := &models.User{
		ID: "admin-sso-user-1", Username: "adminuser",
		CreatedAt: now, UpdatedAt: now,
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

func withAdminPrincipal(req *http.Request, p *auth.Principal) *http.Request {
	return req.WithContext(middleware.WithPrincipal(req.Context(), p))
}

func TestAdminSSO_Create_OIDC_Success(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	oidcCfgBytes, _ := json.Marshal(models.OIDCConfig{
		Issuer:       "https://203.0.113.10",
		ClientID:     "my-client",
		ClientSecret: "my-secret",
	})
	body, _ := json.Marshal(map[string]any{
		"slug":         "my-oidc",
		"kind":         "oidc",
		"display_name": "My OIDC Provider",
		"oidc_config":  json.RawMessage(oidcCfgBytes),
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/sso-providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminPrincipal(req, principal)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&resp)
	if resp["slug"] != "my-oidc" {
		t.Errorf("expected slug=my-oidc, got %v", resp["slug"])
	}

	if resp["display_name"] != "My OIDC Provider" {
		t.Errorf("expected display_name=My OIDC Provider, got %v", resp["display_name"])
	}

	if oidcCfg, ok := resp["oidc_config"]; ok && oidcCfg != nil {
		raw, _ := json.Marshal(oidcCfg)

		var cfg map[string]any

		_ = json.Unmarshal(raw, &cfg)
		if s, ok := cfg["client_secret"].(string); ok && s != "" {
			t.Errorf("response must mask client_secret, got %v", s)
		}
	}
}

func TestAdminSSO_Create_NonAdmin_Forbidden(t *testing.T) {
	db := newMockDatabase()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	cfg := &config.Config{BaseURL: "http://localhost:8080"}
	h := api.NewAdminSSOHandler(db, log, eval, "http://localhost:8080", cfg)
	router := chi.NewRouter()
	h.RegisterAuthed(router)

	ctx := context.Background()
	now := time.Now()

	nonAdmin := &models.User{
		ID: "nonadmin-1", Username: "nonadmin",
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, nonAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	uid := nonAdmin.ID
	principal := &auth.Principal{UserID: &uid, Username: nonAdmin.Username}

	body, _ := json.Marshal(map[string]any{
		"Slug": "x", "Kind": "oidc", "DisplayName": "X",
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/sso-providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminPrincipal(req, principal)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&resp)
	if resp["code"] != "admin_required" {
		t.Errorf("expected code=admin_required, got %v", resp["code"])
	}
}

func TestAdminSSO_Create_InvalidSlug(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	oidcCfgBytes2, _ := json.Marshal(models.OIDCConfig{Issuer: "https://x.com", ClientID: "c", ClientSecret: "s"})
	body, _ := json.Marshal(map[string]any{
		"Slug":        "Invalid-Slug-With-Uppercase!",
		"Kind":        "oidc",
		"DisplayName": "Bad Slug",
		"OIDCConfig":  base64.StdEncoding.EncodeToString(oidcCfgBytes2),
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/sso-providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminPrincipal(req, principal)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid slug, got %d (body: %s)", rw.Code, rw.Body.String())
	}
}

func TestAdminSSO_Create_OIDC_MissingFields(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	missingCfg, _ := json.Marshal(models.OIDCConfig{Issuer: ""})
	body, _ := json.Marshal(map[string]any{
		"Slug":        "valid-slug",
		"Kind":        "oidc",
		"DisplayName": "Missing OIDC Fields",
		"OIDCConfig":  base64.StdEncoding.EncodeToString(missingCfg),
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/sso-providers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminPrincipal(req, principal)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing oidc fields, got %d (body: %s)", rw.Code, rw.Body.String())
	}
}

func TestAdminSSO_List_MasksSecrets(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)
	ctx := context.Background()

	for _, slug := range []string{"prov-a", "prov-b"} {
		cfg, _ := json.Marshal(models.OIDCConfig{
			Issuer: "https://idp.example.com", ClientID: "cid", ClientSecret: "supersecret",
		})
		now := time.Now()
		_ = db.CreateSSOProvider(ctx, &models.SSOProvider{
			Slug: slug, Kind: "oidc", DisplayName: slug,
			Enabled: true, OIDCConfig: cfg, GroupMapping: make(map[string]string),
			CreatedAt: now, UpdatedAt: now,
		})
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/sso-providers", nil)
	req = withAdminPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&resp)

	providers, _ := resp["providers"].([]any)
	if len(providers) < 2 {
		t.Fatalf("expected at least 2 providers, got %d", len(providers))
	}

	for _, pRaw := range providers {
		p, _ := pRaw.(map[string]any)
		if oidcCfgRaw, ok := p["oidc_config"]; ok && oidcCfgRaw != nil {
			cfgBytes, _ := json.Marshal(oidcCfgRaw)

			var cfg map[string]any

			_ = json.Unmarshal(cfgBytes, &cfg)
			if s, ok := cfg["client_secret"].(string); ok && s != "" {
				t.Errorf("provider %v must mask client_secret in list response", p["slug"])
			}
		}
	}
}

func TestAdminSSO_Get_NotFound(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/sso-providers/nonexistent-slug", nil)
	req = withAdminPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body: %s)", rw.Code, rw.Body.String())
	}
}

func TestAdminSSO_Update_Patch_PartialFields(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)
	ctx := context.Background()

	cfg, _ := json.Marshal(models.OIDCConfig{
		Issuer: "https://idp.example.com", ClientID: "cid", ClientSecret: "original-secret",
	})
	now := time.Now()
	_ = db.CreateSSOProvider(ctx, &models.SSOProvider{
		Slug: "patchme", Kind: "oidc", DisplayName: "Old Name",
		Enabled: true, OIDCConfig: cfg, GroupMapping: make(map[string]string),
		CreatedAt: now, UpdatedAt: now,
	})

	body, _ := json.Marshal(map[string]any{
		"display_name": "New Name",
	})
	req := httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/admin/sso-providers/patchme", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&resp)
	if resp["display_name"] != "New Name" {
		t.Errorf("expected display_name=New Name, got %v", resp["display_name"])
	}

	stored, _ := db.GetSSOProviderBySlug(ctx, "patchme")

	var storedCfg models.OIDCConfig

	_ = json.Unmarshal(stored.OIDCConfig, &storedCfg)
	if storedCfg.ClientSecret != "original-secret" {
		t.Errorf("client_secret must not change on display_name patch, got %q", storedCfg.ClientSecret)
	}
}

func TestAdminSSO_Delete_Success(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)
	ctx := context.Background()

	now := time.Now()
	_ = db.CreateSSOProvider(ctx, &models.SSOProvider{
		Slug: "deleteme", Kind: "saml", DisplayName: "Delete Me",
		Enabled: true, SAMLConfig: []byte(`{"idp_metadata_xml":"x"}`),
		GroupMapping: make(map[string]string), CreatedAt: now, UpdatedAt: now,
	})

	req := httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/admin/sso-providers/deleteme", nil)
	req = withAdminPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	gone, _ := db.GetSSOProviderBySlug(ctx, "deleteme")
	if gone != nil {
		t.Error("provider should be deleted from db")
	}
}

func TestAdminSSO_Test_OIDC_DiscoveryFails(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)
	ctx := context.Background()

	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close() // nothing listens here anymore

	cfg, _ := json.Marshal(models.OIDCConfig{
		Issuer: closedURL + "/nonexistent-issuer", ClientID: "cid", ClientSecret: "s",
	})
	now := time.Now()
	_ = db.CreateSSOProvider(ctx, &models.SSOProvider{
		Slug: "badissuer", Kind: "oidc", DisplayName: "Bad Issuer",
		Enabled: true, OIDCConfig: cfg, GroupMapping: make(map[string]string),
		CreatedAt: now, UpdatedAt: now,
	})

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/sso-providers/badissuer/test", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 with ok=false, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&resp)
	if resp["ok"] != false {
		t.Errorf("expected ok=false, got %v", resp["ok"])
	}

	if resp["error"] != "oidc_discovery_failed" {
		t.Errorf("expected error=oidc_discovery_failed, got %v", resp["error"])
	}
}
