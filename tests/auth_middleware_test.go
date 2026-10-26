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

	"github.com/openartifact/openartifact/internal/passwd"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newMWLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("info", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	return log
}

func setupMWUser(t *testing.T, db *MockDatabase) (*models.User, string) {
	t.Helper()

	ctx := context.Background()

	hash, err := passwd.HashPassword("mw-password-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	now := time.Now()
	user := &models.User{
		ID:           "mw-user-1",
		Username:     "mwuser",
		PasswordHash: &hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	saved, err := db.CreateUser(ctx, user)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	target := &models.PermissionTarget{
		ID:               "mw-allow-all",
		Name:             "mw-allow-all",
		RepositoryFilter: ".*",
		PathPattern:      ".*",
		Actions:          []string{"read", "write", "delete", "manage"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if _, err := db.CreatePermissionTarget(ctx, target); err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	if err := db.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: target.ID,
		PrincipalType:      "user",
		PrincipalID:        saved.ID,
	}); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	cookieValue, _, err := auth.CreateSession(ctx, db, saved.ID, "127.0.0.1", "test-ua")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return saved, cookieValue
}

func newRBACMiddleware(db *MockDatabase, cfg middleware.RBACConfig, log *logger.Logger) func(http.Handler) http.Handler {
	eval := auth.NewEvaluator(db, log, cfg.AllowAnonymousRead)
	return middleware.RBACMiddleware(cfg, db, eval, log)
}

func noopHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})
}

func TestMiddleware_CookieTakesPriorityOverBearer(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	log := newMWLogger(t)
	user, cookieValue := setupMWUser(t, db)

	hash2, _ := passwd.HashPassword("other-pass-456")
	now := time.Now()

	otherUser := &models.User{
		ID:           "mw-user-2",
		Username:     "otheruser",
		CreatedAt:    now,
		UpdatedAt:    now,
		PasswordHash: &hash2,
	}
	if _, err := db.CreateUser(ctx, otherUser); err != nil {
		t.Fatalf("CreateUser other: %v", err)
	}

	tokenStr, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "tok", otherUser.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	cfg := middleware.RBACConfig{AllowAnonymousRead: false}
	mw := newRBACMiddleware(db, cfg, log)

	var capturedPrincipal *auth.Principal

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPrincipal = middleware.PrincipalFromContext(r.Context())

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/test/file", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieValue})
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}

	if capturedPrincipal == nil {
		t.Fatal("no principal in context")
	}

	if capturedPrincipal.Username != user.Username {
		t.Errorf("expected cookie user %q, got %q", user.Username, capturedPrincipal.Username)
	}
}

func TestMiddleware_BearerTokenResolvedUser(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	log := newMWLogger(t)

	hash, _ := passwd.HashPassword("bearer-pass-789")
	now := time.Now()

	user := &models.User{
		ID:           "bearer-user",
		Username:     "beareruser",
		PasswordHash: &hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	target := &models.PermissionTarget{
		ID:               "bearer-allow",
		Name:             "bearer-allow",
		RepositoryFilter: ".*",
		PathPattern:      ".*",
		Actions:          []string{"read", "write", "delete", "manage"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if _, err := db.CreatePermissionTarget(ctx, target); err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	if err := db.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: target.ID,
		PrincipalType:      "user",
		PrincipalID:        user.ID,
	}); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	tokenStr, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "tok", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	cfg := middleware.RBACConfig{AllowAnonymousRead: false}
	mw := newRBACMiddleware(db, cfg, log)

	var capturedPrincipal *auth.Principal

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPrincipal = middleware.PrincipalFromContext(r.Context())

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/test/file", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	if capturedPrincipal == nil || capturedPrincipal.Username != user.Username {
		t.Errorf("expected bearer principal %q", user.Username)
	}
}

func TestMiddleware_LegacyAPIKey(t *testing.T) {
	db := newMockDatabase()
	log := newMWLogger(t)

	cfg := middleware.RBACConfig{
		LegacyAPIKey:        "secret-legacy-key",
		LegacyAPIKeyEnabled: true,
		AllowAnonymousRead:  false,
	}
	mw := newRBACMiddleware(db, cfg, log)

	var capturedPrincipal *auth.Principal

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPrincipal = middleware.PrincipalFromContext(r.Context())

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/test/file", nil)
	req.Header.Set("X-Api-Key", "secret-legacy-key")

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 for legacy key, got %d", rw.Code)
	}

	if capturedPrincipal == nil {
		t.Fatal("no principal in context for legacy key")
	}

	if !capturedPrincipal.IsLegacy {
		t.Error("expected IsLegacy=true for legacy key principal")
	}
}

func TestMiddleware_AnonymousRead_Allowed(t *testing.T) {
	db := newMockDatabase()
	log := newMWLogger(t)

	cfg := middleware.RBACConfig{AllowAnonymousRead: true}
	mw := newRBACMiddleware(db, cfg, log)

	handler := mw(noopHandler(http.StatusOK))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/myrepo/file.rpm", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 for anonymous read when enabled, got %d (body: %s)", rw.Code, rw.Body.String())
	}
}

func TestMiddleware_AnonymousRead_Denied(t *testing.T) {
	db := newMockDatabase()
	log := newMWLogger(t)

	cfg := middleware.RBACConfig{AllowAnonymousRead: false}
	mw := newRBACMiddleware(db, cfg, log)

	handler := mw(noopHandler(http.StatusOK))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/myrepo/file.rpm", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous read when disabled, got %d", rw.Code)
	}

	if rw.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header on 401")
	}
}

// docker/podman follow the first Bearer challenge to a token service; the
// registry has none, so /v2/ must challenge with Basic only.
func TestMiddleware_RegistryChallengeIsBasicOnly(t *testing.T) {
	db := newMockDatabase()
	mw := newRBACMiddleware(db, middleware.RBACConfig{AllowAnonymousRead: false}, newMWLogger(t))
	handler := mw(noopHandler(http.StatusOK))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/v2/", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}

	got := rw.Header().Values("WWW-Authenticate")
	if len(got) != 1 || !strings.HasPrefix(got[0], "Basic ") {
		t.Fatalf("expected a single Basic challenge on /v2/, got %q", got)
	}
}

func TestMiddleware_AnonymousWrite_Denied(t *testing.T) {
	db := newMockDatabase()
	log := newMWLogger(t)

	cfg := middleware.RBACConfig{AllowAnonymousRead: true}
	mw := newRBACMiddleware(db, cfg, log)

	handler := mw(noopHandler(http.StatusOK))

	req := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/myrepo/file.rpm", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous write, got %d", rw.Code)
	}
}

func TestMiddleware_AuthenticatedDenied_403(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	log := newMWLogger(t)

	now := time.Now()

	user := &models.User{
		ID:        "denied-user",
		Username:  "denieduser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cookieValue, _, err := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := middleware.RBACConfig{AllowAnonymousRead: false}
	mw := newRBACMiddleware(db, cfg, log)

	handler := mw(noopHandler(http.StatusOK))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/myrepo/file.rpm", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieValue})

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for authenticated but unauthorized user, got %d", rw.Code)
	}
}

func TestMiddleware_MustChangePassword_Blocked(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	log := newMWLogger(t)

	now := time.Now()

	user := &models.User{
		ID:                 "must-change-user",
		Username:           "mustchange",
		MustChangePassword: true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cookieValue, _, err := auth.CreateSession(ctx, db, user.ID, "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := middleware.RBACConfig{AllowAnonymousRead: false}
	mw := newRBACMiddleware(db, cfg, log)

	handler := mw(noopHandler(http.StatusOK))

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/myrepo/file.rpm", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieValue})

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for must_change_password, got %d", rw.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rw.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if body["error"] != "password_change_required" {
		t.Errorf("expected error=password_change_required, got %q", body["error"])
	}
}

func TestMiddleware_MustChangePassword_BypassChangePasswordRoute(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	log := newMWLogger(t)

	now := time.Now()
	user := &models.User{
		ID:                 "must-change-bypass",
		Username:           "mustchangebypass",
		MustChangePassword: true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	saved, err := db.CreateUser(ctx, user)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	target := &models.PermissionTarget{
		ID:               "bypass-allow-manage",
		Name:             "bypass-allow-manage",
		RepositoryFilter: ".*",
		PathPattern:      ".*",
		Actions:          []string{"read", "write", "delete", "manage"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if _, err := db.CreatePermissionTarget(ctx, target); err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	if err := db.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: target.ID,
		PrincipalType:      "user",
		PrincipalID:        saved.ID,
	}); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	cookieValue, _, err := auth.CreateSession(ctx, db, saved.ID, "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := middleware.RBACConfig{AllowAnonymousRead: false}
	mw := newRBACMiddleware(db, cfg, log)

	reached := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/change-password", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieValue})

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	if !reached {
		t.Fatalf("expected handler to be reached for change-password bypass (got %d)", rw.Code)
	}
}
