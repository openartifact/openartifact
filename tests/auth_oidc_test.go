// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

func seedOIDCProvider(t *testing.T, db *MockDatabase, slug, issuerURL string) *models.SSOProvider {
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

	p := &models.SSOProvider{
		ID:                 "oidc-provider-test",
		Slug:               slug,
		Kind:               "oidc",
		DisplayName:        "Test OIDC",
		Enabled:            true,
		OIDCConfig:         cfg,
		GroupMapping:       make(map[string]string),
		AutoProvisionUsers: true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := db.CreateSSOProvider(context.Background(), p); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	return p
}

func setupOIDCRouter(t *testing.T, db *MockDatabase, baseURL string) chi.Router {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewOIDCHandler(db, log, eval, []byte("test-hmac-key"), baseURL)
	router := chi.NewRouter()
	h.RegisterPublic(router)

	return router
}

func TestOIDC_LoginRedirectsToIdP(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()
	seedOIDCProvider(t, db, "test", oidcSrv.Server.URL)

	router := setupOIDCRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/oidc/test/login", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	loc := rw.Header().Get("Location")
	if !strings.HasPrefix(loc, oidcSrv.Server.URL) {
		t.Errorf("Location %q does not start with IdP URL %q", loc, oidcSrv.Server.URL)
	}

	if !strings.Contains(loc, "code_challenge=") {
		t.Error("Location missing code_challenge")
	}

	if !strings.Contains(loc, "code_challenge_method=S256") {
		t.Error("Location missing code_challenge_method=S256")
	}

	if !strings.Contains(loc, "nonce=") {
		t.Error("Location missing nonce")
	}

	cookies := rw.Result().Cookies()

	cookieNames := make(map[string]string)
	for _, c := range cookies {
		cookieNames[c.Name] = c.Value
	}

	for _, name := range []string{"oa_oidc_pkce_test", "oa_oidc_state_test", "oa_oidc_nonce_test"} {
		if _, ok := cookieNames[name]; !ok {
			t.Errorf("missing cookie %q", name)
		}
	}

	if stateVal, ok := cookieNames["oa_oidc_state_test"]; ok {
		if !strings.Contains(loc, "state="+stateVal) {
			t.Errorf("Location state param does not match state cookie value")
		}
	}
}

func TestOIDC_CallbackHappyPath_CreatesSession(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()
	seedOIDCProvider(t, db, "test", oidcSrv.Server.URL)

	nonce := "test-nonce-abc123"
	idToken := oidcSrv.signIDToken(t, map[string]any{
		"sub":                "user-ext-001",
		"preferred_username": "oidcuser",
		"email":              "oidcuser@example.com",
		"nonce":              nonce,
		"aud":                []string{"test-client"},
	})
	oidcSrv.addCode(t, "valid-code-1", idToken)

	router := setupOIDCRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(
		context.Background(),
		"GET",
		"/api/v1/auth/sso/oidc/test/callback?code=valid-code-1&state=test-state-xyz",
		nil,
	)
	req.AddCookie(&http.Cookie{Name: "oa_oidc_pkce_test", Value: "fake-verifier"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_state_test", Value: "test-state-xyz"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_nonce_test", Value: nonce})

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	if rw.Header().Get("Location") != "/" {
		t.Errorf("expected redirect to /, got %q", rw.Header().Get("Location"))
	}

	sessionFound := false

	for _, c := range rw.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			sessionFound = true
		}
	}

	if !sessionFound {
		t.Error("expected oa_session cookie to be set")
	}

	if db.authUsersByName["oidcuser"] == nil {
		t.Error("expected user oidcuser to be provisioned in mockDB")
	}
}

func TestOIDC_Callback_StateMismatch(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()
	seedOIDCProvider(t, db, "test", oidcSrv.Server.URL)

	router := setupOIDCRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(
		context.Background(),
		"GET",
		"/api/v1/auth/sso/oidc/test/callback?code=some-code&state=wrong-state",
		nil,
	)
	req.AddCookie(&http.Cookie{Name: "oa_oidc_pkce_test", Value: "fake-verifier"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_state_test", Value: "correct-state"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_nonce_test", Value: "some-nonce"})

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	assertSSOError(t, rw, "state_mismatch")
}

// The IdP's `error` parameter is attacker-controlled (anyone can send the
// browser to the callback): only access_denied survives as a code, every other
// value is reported as oidc_error and never echoed into the login-page URL.
func TestOIDC_Callback_ProviderError(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()
	seedOIDCProvider(t, db, "test", oidcSrv.Server.URL)

	router := setupOIDCRouter(t, db, "http://localhost:8080")

	for _, tt := range []struct{ idpError, want string }{
		{"access_denied", "access_denied"},
		{"server_error", "oidc_error"},
		{"temporarily_unavailable", "oidc_error"},
		{"<script>alert(1)</script>", "oidc_error"},
		{"sso_login_denied", "oidc_error"},
	} {
		t.Run(tt.idpError, func(t *testing.T) {
			target := "/api/v1/auth/sso/oidc/test/callback?error=" + url.QueryEscape(tt.idpError) + "&error_description=boom"
			req := httptest.NewRequestWithContext(context.Background(), "GET", target, nil)
			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, req)

			assertSSOError(t, rw, tt.want)

			if loc := rw.Header().Get("Location"); loc != "/login?sso_error="+tt.want {
				t.Errorf("Location = %q, want only the sso_error code", loc)
			}
		})
	}
}

func TestOIDC_Callback_NonceMismatch(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()
	seedOIDCProvider(t, db, "test", oidcSrv.Server.URL)

	wrongNonce := "nonce-in-token"
	idToken := oidcSrv.signIDToken(t, map[string]any{
		"sub":   "user-ext-002",
		"nonce": wrongNonce,
		"aud":   []string{"test-client"},
	})
	oidcSrv.addCode(t, "nonce-code", idToken)

	router := setupOIDCRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/oidc/test/callback?code=nonce-code&state=st1", nil)
	req.AddCookie(&http.Cookie{Name: "oa_oidc_pkce_test", Value: "fake-verifier"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_state_test", Value: "st1"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_nonce_test", Value: "different-nonce"})

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	assertSSOError(t, rw, "nonce_mismatch")
}

func TestOIDC_Callback_ProviderNotFound(t *testing.T) {
	db := newMockDatabase()
	router := setupOIDCRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/oidc/nosuchslug/callback?code=x&state=y", nil)
	req.AddCookie(&http.Cookie{Name: "oa_oidc_pkce_nosuchslug", Value: "v"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_state_nosuchslug", Value: "y"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_nonce_nosuchslug", Value: "n"})

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	assertSSOError(t, rw, "provider_not_found")
}

func TestOIDC_Logout_ClearsSession(t *testing.T) {
	oidcSrv := newMockOIDCServer(t)
	db := newMockDatabase()
	seedOIDCProvider(t, db, "test", oidcSrv.Server.URL)

	ctx := context.Background()
	userID := "logout-user-1"
	now := time.Now()

	user := &models.User{
		ID: userID, Username: "logoutuser",
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cookieValue, _, err := auth.CreateSession(ctx, db, userID, "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	router := setupOIDCRouter(t, db, "http://localhost:8080")
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/sso/oidc/test/logout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieValue})

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent && rw.Code != http.StatusOK {
		t.Fatalf("expected 204 or 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	sessionCleared := false

	for _, c := range rw.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge < 0 {
			sessionCleared = true
		}
	}

	if !sessionCleared {
		t.Error("expected oa_session cookie to be cleared (MaxAge < 0)")
	}
}
