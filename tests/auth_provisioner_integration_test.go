// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
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
	"github.com/openartifact/openartifact/internal/database/models"
)

func setupOIDCCallbackRouter(t *testing.T, db *MockDatabase, oidcSrv *mockOIDCServer) chi.Router {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewOIDCHandler(db, log, eval, []byte("test-hmac-key"), "http://localhost:8080")
	router := chi.NewRouter()
	h.RegisterPublic(router)

	return router
}

func seedOIDCProviderWithGroupMapping(t *testing.T, db *MockDatabase, slug, issuerURL string, autoProvision bool, groupMapping map[string]string, linkByEmail bool) *models.SSOProvider {
	t.Helper()

	cfg, err := json.Marshal(models.OIDCConfig{
		Issuer:       issuerURL,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
	})
	if err != nil {
		t.Fatalf("marshal oidc config: %v", err)
	}

	now := time.Now()

	if groupMapping == nil {
		groupMapping = make(map[string]string)
	}

	p := &models.SSOProvider{
		Slug:               slug,
		Kind:               "oidc",
		DisplayName:        "Provisioner Test",
		Enabled:            true,
		OIDCConfig:         cfg,
		GroupMapping:       groupMapping,
		AutoProvisionUsers: autoProvision,
		LinkByEmail:        linkByEmail,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := db.CreateSSOProvider(context.Background(), p); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	return p
}

func doOIDCCallback(t *testing.T, router chi.Router, slug, code, state, nonce string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), "GET",
		"/api/v1/auth/sso/oidc/"+slug+"/callback?code="+code+"&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: "oa_oidc_pkce_" + slug, Value: "fake-verifier"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_state_" + slug, Value: state})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_nonce_" + slug, Value: nonce})

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func TestProvisioning_OIDC_NewUser_GroupSync(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()

	ctx := context.Background()

	devGroup, err := db.CreateGroup(ctx, &models.Group{
		ID:        "dev-group-1",
		Name:      "developers",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	seedOIDCProviderWithGroupMapping(t, db, "prov", oidcSrv.Server.URL, true,
		map[string]string{"developers": devGroup.Name}, false)

	nonce := "prov-nonce-1"
	idToken := oidcSrv.signIDToken(t, map[string]any{
		"sub":                "new-user-ext-1",
		"preferred_username": "newdevuser",
		"email":              "newdev@example.com",
		"nonce":              nonce,
		"aud":                []string{"test-client"},
		"groups":             []string{"developers", "ops"},
	})
	oidcSrv.addCode(t, "prov-code-1", idToken)

	router := setupOIDCCallbackRouter(t, db, oidcSrv)
	rw := doOIDCCallback(t, router, "prov", "prov-code-1", "prov-state-1", nonce)

	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	user := db.authUsersByName["newdevuser"]
	if user == nil {
		t.Fatal("expected user newdevuser to be created")
	}

	ssoGroups := db.authUserGroupsSSO[user.ID]
	if !ssoGroups[devGroup.ID] {
		t.Errorf("expected user to be in developers group (ID %q), got sso groups: %v", devGroup.ID, ssoGroups)
	}
}

func TestProvisioning_OIDC_LinkByEmail(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()

	ctx := context.Background()
	email := "alice@example.com"

	existingUser, err := db.CreateUser(ctx, &models.User{
		ID:        "alice-user-1",
		Username:  "alice",
		Email:     &email,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	seedOIDCProviderWithGroupMapping(t, db, "linkprov", oidcSrv.Server.URL, false, nil, true)

	nonce := "link-nonce-1"
	idToken := oidcSrv.signIDToken(t, map[string]any{
		"sub":                "ext-alice-001",
		"preferred_username": "alice-oidc",
		"email":              email,
		"email_verified":     true,
		"nonce":              nonce,
		"aud":                []string{"test-client"},
	})
	oidcSrv.addCode(t, "link-code-1", idToken)

	router := setupOIDCCallbackRouter(t, db, oidcSrv)
	rw := doOIDCCallback(t, router, "linkprov", "link-code-1", "link-state-1", nonce)

	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302 after link-by-email, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	updated := db.authUsers[existingUser.ID]
	if updated == nil {
		t.Fatal("existing user not found in db")
	}

	if updated.ExternalID == nil || *updated.ExternalID != "ext-alice-001" {
		t.Errorf("expected external_id=ext-alice-001, got %v", updated.ExternalID)
	}

	if updated.ProviderSlug == nil || *updated.ProviderSlug != "linkprov" {
		t.Errorf("expected provider_slug=linkprov, got %v", updated.ProviderSlug)
	}
}

func TestProvisioning_DisabledUser_Blocked(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()

	ctx := context.Background()
	extID := "disabled-ext-001"
	provSlug := "disabledprov"

	_, err := db.CreateUser(ctx, &models.User{
		ID:           "disabled-user-1",
		Username:     "disableduser",
		Disabled:     true,
		ExternalID:   &extID,
		ProviderSlug: &provSlug,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	db.userExternalIDs[extID+":"+provSlug] = db.authUsers["disabled-user-1"]

	seedOIDCProviderWithGroupMapping(t, db, provSlug, oidcSrv.Server.URL, true, nil, false)

	nonce := "disabled-nonce-1"
	idToken := oidcSrv.signIDToken(t, map[string]any{
		"sub":                extID,
		"preferred_username": "disableduser",
		"nonce":              nonce,
		"aud":                []string{"test-client"},
	})
	oidcSrv.addCode(t, "disabled-code-1", idToken)

	router := setupOIDCCallbackRouter(t, db, oidcSrv)
	rw := doOIDCCallback(t, router, provSlug, "disabled-code-1", "disabled-state-1", nonce)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disabled user, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&resp)
	if resp["code"] != "account_disabled" {
		t.Errorf("expected code=account_disabled, got %v", resp["code"])
	}
}

func TestProvisioning_OIDC_LinkByEmail_UnverifiedEmailNotLinked(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()

	ctx := context.Background()
	email := "admin@example.com"

	existingUser, err := db.CreateUser(ctx, &models.User{
		ID:        "admin-user-1",
		Username:  "admin",
		Email:     &email,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	seedOIDCProviderWithGroupMapping(t, db, "unverifiedprov", oidcSrv.Server.URL, true, nil, true)

	// No email_verified claim at all: the IdP asserts nothing about the address.
	nonce := "unv-nonce-1"
	idToken := oidcSrv.signIDToken(t, map[string]any{
		"sub":                "ext-attacker-001",
		"preferred_username": "attacker",
		"email":              email,
		"nonce":              nonce,
		"aud":                []string{"test-client"},
	})
	oidcSrv.addCode(t, "unv-code-1", idToken)

	router := setupOIDCCallbackRouter(t, db, oidcSrv)
	rw := doOIDCCallback(t, router, "unverifiedprov", "unv-code-1", "unv-state-1", nonce)

	if rw.Code == http.StatusFound {
		t.Fatalf("login with an unverified email must be denied, got 302")
	}

	// The denial is deliberately generic: it must not reveal that the address
	// belongs to a local account.
	if rw.Code != http.StatusForbidden || !strings.Contains(rw.Body.String(), `"sso_login_denied"`) {
		t.Errorf("expected 403 sso_login_denied, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	if got := db.authUsers[existingUser.ID]; got == nil || got.ExternalID != nil {
		t.Errorf("existing account must not be linked, got %+v", got)
	}
}

func TestOIDC_DisabledProvider_Rejected(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()

	p := seedOIDCProviderWithGroupMapping(t, db, "offprov", oidcSrv.Server.URL, true, nil, false)
	p.Enabled = false

	if err := db.UpdateSSOProvider(context.Background(), p); err != nil {
		t.Fatalf("UpdateSSOProvider: %v", err)
	}

	router := setupOIDCCallbackRouter(t, db, oidcSrv)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/oidc/offprov/login", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled provider login, got %d", rw.Code)
	}

	nonce := "off-nonce"
	idToken := oidcSrv.signIDToken(t, map[string]any{
		"sub": "ext-off", "preferred_username": "off", "nonce": nonce, "aud": []string{"test-client"},
	})
	oidcSrv.addCode(t, "off-code", idToken)

	rw = doOIDCCallback(t, router, "offprov", "off-code", "off-state", nonce)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled provider callback, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	if db.authUsersByName["off"] != nil {
		t.Error("disabled provider must not provision users")
	}
}
