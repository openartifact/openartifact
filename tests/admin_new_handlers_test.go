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

// detailsJSON creates a DetailsJSON field with actor and message.
func detailsJSON(actor, message string) json.RawMessage {
	m := map[string]string{"actor": actor}
	if message != "" {
		m["message"] = message
	}
	b, _ := json.Marshal(m)
	return b
}

// ============================================================================
// Admin Audit Handler Tests
// ============================================================================

func setupAdminAuditRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewAdminAuditHandler(db, log, eval)

	router := chi.NewRouter()
	h.Register(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "audit-admin-1",
		Username:  "auditadmin",
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

func TestAdminAudit_ListEmpty(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminAuditRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/audit", nil)
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

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("data not found or not an array")
	}

	if len(data) != 0 {
		t.Errorf("expected empty data array, got %d items", len(data))
	}
}

func TestAdminAudit_ListWithData(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminAuditRouter(t, db)

	ctx := context.Background()
	now := time.Now()
	log1 := &models.AuditLog{
		ID:          "audit-1",
		Action:      "create",
		ActorID:     new("admin"),
		DetailsJSON: detailsJSON("admin", "created artifact"),
		CreatedAt:   now,
	}
	log2 := &models.AuditLog{
		ID:          "audit-2",
		Action:      "delete",
		ActorID:     new("admin"),
		DetailsJSON: detailsJSON("admin", "deleted artifact"),
		CreatedAt:   now.Add(time.Second),
	}

	if err := db.InsertAuditLog(ctx, log1); err != nil {
		t.Fatalf("InsertAuditLog: %v", err)
	}

	if err := db.InsertAuditLog(ctx, log2); err != nil {
		t.Fatalf("InsertAuditLog: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/audit", nil)
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

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("data not found or not an array")
	}

	if len(data) != 2 {
		t.Errorf("expected 2 items, got %d", len(data))
	}

	totalVal, ok := resp["total"].(float64)
	if !ok {
		t.Fatalf("total not found or not a float64")
	}

	total := int(totalVal)
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
}

func TestAdminAudit_ListFilterByActor(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminAuditRouter(t, db)

	ctx := context.Background()
	now := time.Now()
	log1 := &models.AuditLog{
		ID:          "audit-1",
		Action:      "create",
		ActorID:     new("admin"),
		DetailsJSON: detailsJSON("admin", "created artifact"),
		CreatedAt:   now,
	}
	log2 := &models.AuditLog{
		ID:          "audit-2",
		Action:      "delete",
		ActorID:     new("user"),
		DetailsJSON: detailsJSON("user", "deleted artifact"),
		CreatedAt:   now.Add(time.Second),
	}

	if err := db.InsertAuditLog(ctx, log1); err != nil {
		t.Fatalf("InsertAuditLog: %v", err)
	}

	if err := db.InsertAuditLog(ctx, log2); err != nil {
		t.Fatalf("InsertAuditLog: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/audit?actor=admin", nil)
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

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("data not found or not an array")
	}

	if len(data) != 1 {
		t.Errorf("expected 1 item, got %d", len(data))
	}

	item, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("item not found or not a map")
	}

	if item["actor"] != "admin" {
		t.Errorf("expected actor=admin, got %v", item["actor"])
	}
}

func TestAdminAudit_RequiresAdmin(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupAdminAuditRouter(t, db)

	ctx := context.Background()
	now := time.Now()

	normalUser := &models.User{
		ID:        "audit-user-1",
		Username:  "normaluser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, normalUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	uid := normalUser.ID
	normalPrincipal := &auth.Principal{
		UserID:      &uid,
		Username:    normalUser.Username,
		IsAnonymous: false,
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/audit", nil)
	req = withPrincipal(req, normalPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rw.Code)
	}
}

// ============================================================================
// Admin GPG Handler Tests
// ============================================================================

func setupGPGRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewAdminGPGHandler(db, log, eval)

	router := chi.NewRouter()
	h.Register(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "gpg-admin-1",
		Username:  "gpgadmin",
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

func TestAdminGPG_ListEmpty(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupGPGRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/gpg/keys", nil)
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

	keys, ok := resp["keys"].([]any)
	if !ok {
		t.Fatalf("keys not found or not an array")
	}

	if len(keys) != 0 {
		t.Errorf("expected empty keys array, got %d items", len(keys))
	}
}

func TestAdminGPG_ImportNotImplemented(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupGPGRouter(t, db)

	body := `{"key":"test-key"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/gpg/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rw.Code)
	}
}

func TestAdminGPG_GenerateNotImplemented(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupGPGRouter(t, db)

	body := `{}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/admin/gpg/keys/generate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rw.Code)
	}
}

func TestAdminGPG_DeleteNotImplemented(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupGPGRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/admin/gpg/keys/test-id", nil)
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rw.Code)
	}
}

func TestAdminGPG_RequiresAdmin(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGPGRouter(t, db)

	ctx := context.Background()
	now := time.Now()

	normalUser := &models.User{
		ID:        "gpg-user-1",
		Username:  "normaluser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, normalUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	uid := normalUser.ID
	normalPrincipal := &auth.Principal{
		UserID:      &uid,
		Username:    normalUser.Username,
		IsAnonymous: false,
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/gpg/keys", nil)
	req = withPrincipal(req, normalPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rw.Code)
	}
}

// ============================================================================
// Self Tokens Handler Tests
// ============================================================================

func setupSelfTokensRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewSelfTokensHandler(db, log, eval)

	router := chi.NewRouter()
	h.Register(router)

	ctx := context.Background()
	now := time.Now()

	normalUser := &models.User{
		ID:        "token-user-1",
		Username:  "tokenuser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, normalUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	uid := normalUser.ID
	principal := &auth.Principal{
		UserID:      &uid,
		Username:    normalUser.Username,
		IsAnonymous: false,
	}

	return router, principal
}

func TestSelfTokens_ListEmpty(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSelfTokensRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/tokens", nil)
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

	data := resp["data"].([]any)
	if len(data) != 0 {
		t.Errorf("expected empty data array, got %d items", len(data))
	}
}

func TestSelfTokens_Create(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSelfTokensRouter(t, db)

	body := `{"name":"test-token","kind":"access"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, hasToken := resp["token"]; !hasToken {
		t.Error("response must include token field")
	}

	if resp["name"] != "test-token" {
		t.Errorf("expected name=test-token, got %v", resp["name"])
	}

	if resp["kind"] != "access" {
		t.Errorf("expected kind=access, got %v", resp["kind"])
	}
}

func TestSelfTokens_CreateAndList(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSelfTokensRouter(t, db)

	// Create a token
	body := `{"name":"test-token","kind":"access"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rw.Code)
	}

	var createResp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// List tokens
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/tokens", nil)
	req = withPrincipal(req, principal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}

	var listResp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	data := listResp["data"].([]any)
	if len(data) != 1 {
		t.Errorf("expected 1 token, got %d", len(data))
	}

	token := data[0].(map[string]any)
	if token["name"] != "test-token" {
		t.Errorf("expected name=test-token, got %v", token["name"])
	}
}

func TestSelfTokens_Revoke(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSelfTokensRouter(t, db)

	// Create a token
	body := `{"name":"test-token","kind":"access"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, principal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var createResp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	tokenID := createResp["id"].(string)

	// Revoke the token
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/auth/tokens/"+tokenID, nil)
	req = withPrincipal(req, principal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}

	// Verify token is revoked by listing
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/tokens", nil)
	req = withPrincipal(req, principal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var listResp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	data := listResp["data"].([]any)
	if len(data) != 0 {
		t.Errorf("expected 0 tokens after revoke, got %d", len(data))
	}
}

func TestSelfTokens_RevokeOtherUserToken(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSelfTokensRouter(t, db)

	ctx := context.Background()
	now := time.Now()

	// Create another user
	otherUser := &models.User{
		ID:        "token-user-2",
		Username:  "otheruser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, otherUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a token for the other user
	otherUID := otherUser.ID
	otherPrincipal := &auth.Principal{
		UserID:      &otherUID,
		Username:    otherUser.Username,
		IsAnonymous: false,
	}

	body := `{"name":"other-token","kind":"access"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, otherPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var createResp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	tokenID := createResp["id"].(string)

	// Try to revoke the other user's token with the first user
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/auth/tokens/"+tokenID, nil)
	req = withPrincipal(req, principal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rw.Code)
	}
}

func TestSelfTokens_RequiresAuth(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupSelfTokensRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/tokens", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rw.Code)
	}
}

// ============================================================================
// Admin Tokens Handler Tests
// ============================================================================

func setupAdminTokensRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewAdminTokensHandler(db, log, eval)

	router := chi.NewRouter()
	h.Register(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "2fd45323-03fd-5344-aef6-4001343fcf52",
		Username:  "admintokenadmin",
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

func TestAdminTokens_ListAll(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminTokensRouter(t, db)

	ctx := context.Background()
	now := time.Now()

	// Create a user and a token for them
	user := &models.User{
		ID:        "473dff23-a847-5abe-a91e-0306a99a2da8",
		Username:  "tokenuser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	userPrincipal := &auth.Principal{
		UserID:      &user.ID,
		Username:    user.Username,
		IsAnonymous: false,
	}

	// Create a token via the user endpoint
	body := `{"name":"test-token","kind":"access"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users/"+user.ID+"/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, userPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rw.Code, rw.Body.String())
	}

	// List all tokens as admin
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/tokens", nil)
	req = withPrincipal(req, principal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	data := resp["data"].([]any)
	if len(data) != 1 {
		t.Errorf("expected 1 token, got %d", len(data))
	}

	token := data[0].(map[string]any)
	if token["user_id"] != user.ID {
		t.Errorf("expected user_id=%s, got %v", user.ID, token["user_id"])
	}
}

func TestAdminTokens_AdminRevoke(t *testing.T) {
	db := newMockDatabase()
	router, adminPrincipal := setupAdminTokensRouter(t, db)

	ctx := context.Background()
	now := time.Now()

	// Create a user and a token for them
	user := &models.User{
		ID:        "208b961c-2e65-574e-a9b8-3a38f728c085",
		Username:  "tokenuser2",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	userPrincipal := &auth.Principal{
		UserID:      &user.ID,
		Username:    user.Username,
		IsAnonymous: false,
	}

	// Create a token via the user endpoint
	body := `{"name":"test-token","kind":"access"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users/"+user.ID+"/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, userPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var createResp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	tokenID := createResp["id"].(string)

	// Revoke the token as admin
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/admin/tokens/"+tokenID, nil)
	req = withPrincipal(req, adminPrincipal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminTokens_RequiresAdmin(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupAdminTokensRouter(t, db)

	ctx := context.Background()
	now := time.Now()

	normalUser := &models.User{
		ID:        "admin-token-user-3",
		Username:  "normaluser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, normalUser); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	uid := normalUser.ID
	normalPrincipal := &auth.Principal{
		UserID:      &uid,
		Username:    normalUser.Username,
		IsAnonymous: false,
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/admin/tokens", nil)
	req = withPrincipal(req, normalPrincipal)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rw.Code)
	}
}
