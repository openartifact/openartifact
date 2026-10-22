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
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// ============================================================================
// Admin Users Handler Tests
// ============================================================================

// defaultPrincipalMiddleware authenticates every request as the given user
// (a session-style principal) unless the test already attached a principal.
// The handlers authorize on their own, so tests exercising the happy path run
// as the seeded admin and tests exercising denial attach a different principal.
func defaultPrincipalMiddleware(userID, username string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if middleware.PrincipalFromContext(r.Context()) == nil {
				uid := userID
				p := &auth.Principal{UserID: &uid, Username: username, Credential: auth.CredentialSession}
				r = r.WithContext(middleware.WithPrincipal(r.Context(), p))
			}

			next.ServeHTTP(w, r)
		})
	}
}

func setupUsersRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewAdminUsersHandler(db, log, eval)

	router := chi.NewRouter()
	router.Use(defaultPrincipalMiddleware("36d86ead-884f-5ded-9408-ffc1c86bdbfa", "adminusers"))
	h.Register(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "36d86ead-884f-5ded-9408-ffc1c86bdbfa",
		Username:  "adminusers",
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

func TestAdminUsers_Create(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)

	body := `{"username":"newuser","password":"pass123456-valid"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["username"] != "newuser" {
		t.Errorf("expected username=newuser, got %v", resp["username"])
	}

	if _, hasHash := resp["password_hash"]; hasHash {
		t.Error("response must not include password_hash")
	}
}

func TestAdminUsers_CreateInvalidUsername(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)

	body := `{"username":"ab","password":"pass123456-valid"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

func TestAdminUsers_List(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)

	// Create 3 users
	for i := 1; i <= 3; i++ {
		body := `{"username":"user` + string(rune(48+i)) + `","password":"pass123456-valid"}`
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)

		if rw.Code != http.StatusCreated {
			t.Fatalf("failed to create user %d: %d", i, rw.Code)
		}
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/users", nil)
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
	if total < 3 {
		t.Errorf("expected at least 3 users, got %d", total)
	}

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("data not found or not an array")
	}

	if len(data) < 3 {
		t.Errorf("expected at least 3 users in data, got %d", len(data))
	}
}

func TestAdminUsers_Get(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)

	// Create a user
	body := `{"username":"getuser","password":"pass123456-valid","email":"test@example.com"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	userIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	userID := userIDVal

	// Get the user
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/users/"+userID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["username"] != "getuser" {
		t.Errorf("expected username=getuser, got %v", resp["username"])
	}

	if resp["email"] != "test@example.com" {
		t.Errorf("expected email=test@example.com, got %v", resp["email"])
	}
}

func TestAdminUsers_Patch(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupUsersRouter(t, db)

	// Create a user
	body := `{"username":"patchuser","password":"pass123456-valid"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	userIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	userID := userIDVal

	// Patch the user
	patchBody := `{"email":"new@test.com"}`
	req = httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/users/"+userID, bytes.NewReader([]byte(patchBody)))
	req.Header.Set("Content-Type", "application/json")
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

	if resp["email"] != "new@test.com" {
		t.Errorf("expected email=new@test.com, got %v", resp["email"])
	}
}

func TestAdminUsers_PatchDisable(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupUsersRouter(t, db)

	// Create a user
	body := `{"username":"disableuser","password":"pass123456-valid"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	userIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	userID := userIDVal

	// Patch to disable
	patchBody := `{"disabled":true}`
	req = httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/users/"+userID, bytes.NewReader([]byte(patchBody)))
	req.Header.Set("Content-Type", "application/json")
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

	if resp["disabled"] != true {
		t.Errorf("expected disabled=true, got %v", resp["disabled"])
	}
}

func TestAdminUsers_Delete(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupUsersRouter(t, db)

	// Create a user
	body := `{"username":"deleteuser","password":"pass123456-valid"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/users", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	userIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	userID := userIDVal

	// Delete the user
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/users/"+userID, nil)
	req = withPrincipal(req, principal)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminUsers_GetNotFound(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/users/nonexistent", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

// ============================================================================
// Admin Groups Handler Tests
// ============================================================================

func setupGroupsRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewAdminGroupsHandler(db, log, eval)

	router := chi.NewRouter()
	router.Use(defaultPrincipalMiddleware("2f99d2db-e55e-5763-b044-8ede7ce71cf5", "admingroups"))
	h.Register(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "2f99d2db-e55e-5763-b044-8ede7ce71cf5",
		Username:  "admingroups",
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

func TestAdminGroups_Create(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	body := `{"name":"test-group"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["name"] != "test-group" {
		t.Errorf("expected name=test-group, got %v", resp["name"])
	}
}

func TestAdminGroups_CreateInvalidName(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	body := `{"name":"a"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

func TestAdminGroups_List(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create 2 groups
	for i := 1; i <= 2; i++ {
		body := `{"name":"group` + string(rune(48+i)) + `"}`
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)

		if rw.Code != http.StatusCreated {
			t.Fatalf("failed to create group %d: %d", i, rw.Code)
		}
	}

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/groups", nil)
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

	if len(data) < 2 {
		t.Errorf("expected at least 2 groups in data, got %d", len(data))
	}
}

func TestAdminGroups_Get(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create a group
	body := `{"name":"getgroup"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	groupIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	groupID := groupIDVal

	// Get the group
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/groups/"+groupID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["name"] != "getgroup" {
		t.Errorf("expected name=getgroup, got %v", resp["name"])
	}
}

func TestAdminGroups_Patch(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create a group
	body := `{"name":"patchgroup"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	groupIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	groupID := groupIDVal

	// Patch the group
	patchBody := `{"name":"renamed"}`
	req = httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/groups/"+groupID, bytes.NewReader([]byte(patchBody)))
	req.Header.Set("Content-Type", "application/json")

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["name"] != "renamed" {
		t.Errorf("expected name=renamed, got %v", resp["name"])
	}
}

func TestAdminGroups_Delete(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create a group
	body := `{"name":"deletegroup"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	groupIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	groupID := groupIDVal

	// Delete the group
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/groups/"+groupID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminGroups_AddMember(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create a user
	ctx := context.Background()
	now := time.Now()

	user := &models.User{
		ID:        "2fe9ecf2-653b-593a-91d1-b6c0e8955a90",
		Username:  "memberuser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a group
	body := `{"name":"membergroup"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	groupIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	groupID := groupIDVal

	// Add member
	req = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups/"+groupID+"/members/"+user.ID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminGroups_RemoveMember(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create a user
	ctx := context.Background()
	now := time.Now()

	user := &models.User{
		ID:        "60a9cdc5-43ce-5826-8213-c9b308c9083a",
		Username:  "removeuser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a group
	body := `{"name":"removegroup"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	groupIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	groupID := groupIDVal

	// Add member
	req = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups/"+groupID+"/members/"+user.ID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	// Remove member
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/groups/"+groupID+"/members/"+user.ID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminGroups_ListMembers(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create users
	ctx := context.Background()
	now := time.Now()
	user1 := &models.User{
		ID:        "1956100b-02f8-562d-ab40-74f707a32d6a",
		Username:  "listuser1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	user2 := &models.User{
		ID:        "8e32a481-3323-5112-bc4b-021894820e59",
		Username:  "listuser2",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if _, err := db.CreateUser(ctx, user1); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := db.CreateUser(ctx, user2); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a group
	body := `{"name":"listmembersgroup"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	groupIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	groupID := groupIDVal

	// Add members
	req = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups/"+groupID+"/members/"+user1.ID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	req = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups/"+groupID+"/members/"+user2.ID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	// List members
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/groups/"+groupID+"/members", nil)
	rw = httptest.NewRecorder()
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
		t.Errorf("expected 2 members, got %d", len(data))
	}
}

func TestAdminGroups_SetMembers(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupGroupsRouter(t, db)

	// Create users
	ctx := context.Background()
	now := time.Now()
	user1 := &models.User{
		ID:        "1e68c38e-690c-53a5-8e2e-1ca72a571fbf",
		Username:  "setuser1",
		CreatedAt: now,
		UpdatedAt: now,
	}
	user2 := &models.User{
		ID:        "a7d38fb1-22d9-5b02-a929-6132f297a2b9",
		Username:  "setuser2",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if _, err := db.CreateUser(ctx, user1); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := db.CreateUser(ctx, user2); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a group
	body := `{"name":"setmembersgroup"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/groups", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	groupIDVal, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	groupID := groupIDVal

	// Set members
	setBody := `{"members":["1e68c38e-690c-53a5-8e2e-1ca72a571fbf","a7d38fb1-22d9-5b02-a929-6132f297a2b9"]}`
	req = httptest.NewRequestWithContext(context.Background(), "PUT", "/api/v1/groups/"+groupID+"/members", bytes.NewReader([]byte(setBody)))
	req.Header.Set("Content-Type", "application/json")

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}

	// Verify members were set
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/groups/"+groupID+"/members", nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	data, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("data not found or not an array")
	}

	if len(data) != 2 {
		t.Errorf("expected 2 members, got %d", len(data))
	}
}

// ============================================================================
// Admin Permissions Handler Tests
// ============================================================================

func setupPermissionsRouter(t *testing.T, db *MockDatabase) (chi.Router, *auth.Principal) {
	t.Helper()
	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewAdminPermissionsHandler(db, log, eval)

	router := chi.NewRouter()
	router.Use(defaultPrincipalMiddleware("92c8b2f5-9766-576b-931c-5213c062f3aa", "adminperms"))
	h.Register(router)

	ctx := context.Background()
	now := time.Now()

	adminUser := &models.User{
		ID:        "92c8b2f5-9766-576b-931c-5213c062f3aa",
		Username:  "adminperms",
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

func TestAdminPermissions_Create(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	body := `{"name":"test-perm","repository_filter":".*","path_pattern":".*","actions":["read","write"]}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if resp["name"] != "test-perm" {
		t.Errorf("expected name=test-perm, got %v", resp["name"])
	}
}

func TestAdminPermissions_List(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/permission-targets", nil)
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
	// Should include seed targets
	if len(data) < 2 {
		t.Errorf("expected at least 2 seed targets, got %d", len(data))
	}
}

func TestAdminPermissions_Get(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	// Create a permission target
	body := `{"name":"get-perm","repository_filter":".*","path_pattern":".*","actions":["read"]}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	permID, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	// Get the permission target
	req = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/permission-targets/"+permID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["name"] != "get-perm" {
		t.Errorf("expected name=get-perm, got %v", resp["name"])
	}
}

func TestAdminPermissions_Patch(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	// Create a permission target
	body := `{"name":"patch-perm","repository_filter":".*","path_pattern":".*","actions":["read"]}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	permID, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	// Patch the permission target
	patchBody := `{"actions":["read","write","delete"]}`
	req = httptest.NewRequestWithContext(context.Background(), "PATCH", "/api/v1/permission-targets/"+permID, bytes.NewReader([]byte(patchBody)))
	req.Header.Set("Content-Type", "application/json")

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	actions, ok := resp["actions"].([]any)
	if !ok {
		t.Fatalf("actions not found or not an array")
	}

	if len(actions) != 3 {
		t.Errorf("expected 3 actions, got %d", len(actions))
	}
}

func TestAdminPermissions_Delete(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	// Create a permission target
	body := `{"name":"delete-perm","repository_filter":".*","path_pattern":".*","actions":["read"]}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	permID, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	// Delete the permission target
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/permission-targets/"+permID, nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminPermissions_Assign(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	// Create a user
	ctx := context.Background()
	now := time.Now()

	user := &models.User{
		ID:        "4ae7c3eb-3f1a-5b39-b85b-357a37b629bd",
		Username:  "assignuser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a permission target
	body := `{"name":"assign-perm","repository_filter":".*","path_pattern":".*","actions":["read"]}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	permID, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	// Assign permission
	assignBody := `{"principal_type":"user","principal_id":"4ae7c3eb-3f1a-5b39-b85b-357a37b629bd"}`
	req = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets/"+permID+"/assignments", bytes.NewReader([]byte(assignBody)))
	req.Header.Set("Content-Type", "application/json")

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminPermissions_Unassign(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	// Create a user
	ctx := context.Background()
	now := time.Now()

	user := &models.User{
		ID:        "da845ce7-5094-5807-b647-f162cf591f0e",
		Username:  "unassignuser",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a permission target
	body := `{"name":"unassign-perm","repository_filter":".*","path_pattern":".*","actions":["read"]}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var created map[string]any
	if err := json.NewDecoder(rw.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	permID, ok := created["id"].(string)
	if !ok {
		t.Fatalf("id not found or not a string")
	}

	// Assign permission
	assignBody := `{"principal_type":"user","principal_id":"da845ce7-5094-5807-b647-f162cf591f0e"}`
	req = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/permission-targets/"+permID+"/assignments", bytes.NewReader([]byte(assignBody)))
	req.Header.Set("Content-Type", "application/json")

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	// Unassign permission
	req = httptest.NewRequestWithContext(context.Background(), "DELETE", "/api/v1/permission-targets/"+permID+"/assignments?principal_type=user&principal_id=da845ce7-5094-5807-b647-f162cf591f0e", nil)
	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}
func (m *MockDatabase) GetDebPackageByID(ctx context.Context, id string) (*models.DebPackage, error) {
	return nil, nil
}
func (m *MockDatabase) ListDebPackagesByRepo(ctx context.Context, repoID string) ([]*models.DebPackage, error) {
	return nil, nil
}
func (m *MockDatabase) GetRpmPackageByID(ctx context.Context, id string) (*models.RpmPackage, error) {
	return nil, nil
}
func (m *MockDatabase) ListRpmPackagesByRepo(ctx context.Context, repoID string) ([]*models.RpmPackage, error) {
	return nil, nil
}
func (m *MockDatabase) CreateDebPackage(ctx context.Context, pkg *models.Component, distID, compID string) error {
	return m.storeUploadedComponent(pkg)
}
func (m *MockDatabase) CreateRpmPackage(ctx context.Context, pkg *models.Component) error {
	return m.storeUploadedComponent(pkg)
}

// storeUploadedComponent records a deb/rpm upload into the shared components
// map so ArtifactExists (dedup checks) can see it. SHA256 falls back to
// ArtifactID because the mock UpsertBlob echoes the sha256 as the blob UUID.
func (m *MockDatabase) storeUploadedComponent(pkg *models.Component) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if pkg.SHA256 == "" {
		pkg.SHA256 = pkg.ArtifactID
	}

	if pkg.ID == "" {
		pkg.ID = pkg.SHA256 + "-" + pkg.RepositoryID
	}

	if _, exists := m.components[pkg.ID]; exists {
		return database.ErrDuplicate
	}

	m.components[pkg.ID] = pkg

	return nil
}
func (m *MockDatabase) UpsertBlob(ctx context.Context, sha256, blobKey string, sizeBytes int64) (string, error) {
	return sha256, nil
}
