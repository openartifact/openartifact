// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// --- helpers ----------------------------------------------------------------

func doJSON(router http.Handler, method, path, body string, p *auth.Principal) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if p != nil {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), p))
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func errorCodeOf(t *testing.T, rw *httptest.ResponseRecorder) string {
	t.Helper()

	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body %q: %v", rw.Body.String(), err)
	}

	return resp.Code
}

// plainUser creates a user with no permission assignments and returns it with
// a session-style principal (a non-admin).
func plainUser(t *testing.T, db *MockDatabase, id, username string) (*models.User, *auth.Principal) {
	t.Helper()

	now := time.Now()
	user := &models.User{ID: id, Username: username, CreatedAt: now, UpdatedAt: now}

	if _, err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	uid := user.ID

	return user, &auth.Principal{UserID: &uid, Username: username, Credential: auth.CredentialSession}
}

func countUserSessions(db *MockDatabase, userID string) int {
	n := 0

	for _, s := range db.authSessions {
		if s.UserID == userID {
			n++
		}
	}

	return n
}

func seedSession(t *testing.T, db *MockDatabase, userID string) string {
	t.Helper()

	cookie, _, err := auth.CreateSession(context.Background(), db, userID, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return cookie
}

// --- #14 password policy + #8 session revocation on admin user paths --------

func TestAdminUsers_CreateRejectsShortPassword(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)

	rw := doJSON(router, "POST", "/api/v1/users", `{"username":"shortpw","password":"short"}`, nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rw.Code, rw.Body.String())
	}

	if code := errorCodeOf(t, rw); code != "invalid_request" {
		t.Errorf("expected code invalid_request, got %q", code)
	}

	if !strings.Contains(rw.Body.String(), auth.ErrPasswordTooShort.Error()) {
		t.Errorf("expected the policy message in the body, got %s", rw.Body.String())
	}

	if u, _ := db.GetUserByUsername(context.Background(), "shortpw"); u != nil {
		t.Error("user must not be created with a rejected password")
	}
}

func TestAdminUsers_PatchPasswordPolicyAndSessionRevocation(t *testing.T) {
	db := newMockDatabase()
	router, adminP := setupUsersRouter(t, db)
	target := createLoginUser(t, db, "victim", "original-password-123", false)

	seedSession(t, db, target.ID)
	seedSession(t, db, target.ID)

	// Too short: rejected, sessions untouched.
	rw := doJSON(router, "PATCH", "/api/v1/users/"+target.ID, `{"password":"short"}`, adminP)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short password, got %d: %s", rw.Code, rw.Body.String())
	}

	if n := countUserSessions(db, target.ID); n != 2 {
		t.Fatalf("rejected reset must not revoke sessions, have %d", n)
	}

	// Valid reset: hash changed and every session revoked.
	rw = doJSON(router, "PATCH", "/api/v1/users/"+target.ID, `{"password":"a-much-better-password-9"}`, adminP)
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	if n := countUserSessions(db, target.ID); n != 0 {
		t.Errorf("admin password reset must revoke all sessions, %d remain", n)
	}

	updated, _ := db.GetUserByID(context.Background(), target.ID)
	if ok, err := auth.VerifyPassword("a-much-better-password-9", *updated.PasswordHash); err != nil || !ok {
		t.Error("new password must verify")
	}
}

func TestAdminUsers_DisableAndDeleteRevokeSessions(t *testing.T) {
	db := newMockDatabase()
	router, adminP := setupUsersRouter(t, db)
	disabled := createLoginUser(t, db, "tobedisabled", "original-password-123", false)
	deleted := createLoginUser(t, db, "tobedeleted", "original-password-123", false)

	seedSession(t, db, disabled.ID)
	seedSession(t, db, deleted.ID)

	rw := doJSON(router, "PATCH", "/api/v1/users/"+disabled.ID, `{"disabled":true}`, adminP)
	if rw.Code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	if n := countUserSessions(db, disabled.ID); n != 0 {
		t.Errorf("disable must revoke sessions, %d remain", n)
	}

	rw = doJSON(router, "DELETE", "/api/v1/users/"+deleted.ID, "", adminP)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", rw.Code, rw.Body.String())
	}

	if n := countUserSessions(db, deleted.ID); n != 0 {
		t.Errorf("delete must revoke sessions, %d remain", n)
	}
}

// --- #17 requireAdmin on admin handlers --------------------------------------

func TestAdminHandlers_RejectNonAdminSession(t *testing.T) {
	db := newMockDatabase()
	usersRouter, _ := setupUsersRouter(t, db)
	groupsRouter, _ := setupGroupsRouter(t, db)
	permsRouter, _ := setupPermissionsRouter(t, db)
	_, normal := plainUser(t, db, "7a4dbbc3-3291-5713-8acd-d8de34f3e7a7", "plainone")

	cases := []struct {
		router http.Handler
		method string
		path   string
		body   string
	}{
		{usersRouter, "GET", "/api/v1/users", ""},
		{usersRouter, "GET", "/api/v1/users/b31a7242-236f-5087-9a6b-e7998cc1846c", ""},
		{usersRouter, "POST", "/api/v1/users", `{"username":"intruder","password":"intruder-password-1"}`},
		{usersRouter, "DELETE", "/api/v1/users/b31a7242-236f-5087-9a6b-e7998cc1846c", ""},
		{groupsRouter, "GET", "/api/v1/groups", ""},
		{groupsRouter, "POST", "/api/v1/groups", `{"name":"evil"}`},
		{groupsRouter, "POST", "/api/v1/groups/67073eed-f57d-56d9-8782-704cd33325ea/members/7a4dbbc3-3291-5713-8acd-d8de34f3e7a7", ""},
		{groupsRouter, "PUT", "/api/v1/groups/67073eed-f57d-56d9-8782-704cd33325ea/members", `{"members":["7a4dbbc3-3291-5713-8acd-d8de34f3e7a7"]}`},
		{permsRouter, "GET", "/api/v1/permission-targets", ""},
		{permsRouter, "POST", "/api/v1/permission-targets", `{"name":"x","repository_filter":"*","path_pattern":"*","actions":["manage"]}`},
		{permsRouter, "POST", "/api/v1/permission-targets/981948bd-de8f-5c65-b90b-23d72cf37aab/assignments", `{"principal_type":"user","principal_id":"7a4dbbc3-3291-5713-8acd-d8de34f3e7a7"}`},
	}

	for _, tc := range cases {
		rw := doJSON(tc.router, tc.method, tc.path, tc.body, normal)
		if rw.Code != http.StatusForbidden {
			t.Errorf("%s %s: expected 403 for a non-admin session, got %d (%s)", tc.method, tc.path, rw.Code, rw.Body.String())
		}
	}

	if u, _ := db.GetUserByUsername(context.Background(), "intruder"); u != nil {
		t.Error("non-admin created a user")
	}
}

func TestAdminUsers_SelfPatchIsLimitedToProfileFields(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)
	user, p := plainUser(t, db, "a3096ebc-f671-500c-8e9e-160e4015b545", "selfone")
	_, other := plainUser(t, db, "b232c7d3-226b-586c-b100-4d1fdf75055a", "selftwo")

	rw := doJSON(router, "PATCH", "/api/v1/users/"+user.ID, `{"full_name":"Self One"}`, p)
	if rw.Code != http.StatusOK {
		t.Fatalf("self profile edit: expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	for _, body := range []string{
		`{"password":"a-brand-new-password-1"}`,
		`{"disabled":true}`,
		`{"must_change_password":false}`,
		// AUTH-3: a self-set email would let the user pre-claim an address
		// and hijack the owner's SSO identity via link_by_email.
		`{"email":"victim@example.com"}`,
	} {
		rw = doJSON(router, "PATCH", "/api/v1/users/"+user.ID, body, p)
		if rw.Code != http.StatusForbidden {
			t.Errorf("self PATCH %s: expected 403, got %d", body, rw.Code)
		}
	}

	rw = doJSON(router, "PATCH", "/api/v1/users/"+user.ID, `{"full_name":"hijack"}`, other)
	if rw.Code != http.StatusForbidden {
		t.Errorf("patching another user as non-admin: expected 403, got %d", rw.Code)
	}

	// A token owned by the same user is not an interactive session.
	tokenP := *p
	tokenP.TokenKind = auth.TokenKindAccess
	tokenP.Credential = auth.CredentialBearerToken

	rw = doJSON(router, "PATCH", "/api/v1/users/"+user.ID, `{"full_name":"via token"}`, &tokenP)
	if rw.Code != http.StatusForbidden {
		t.Errorf("self PATCH via API token: expected 403, got %d", rw.Code)
	}
}

// --- #11 permission target validation ---------------------------------------

func TestAdminPermissions_CreateValidation(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	bad := map[string]string{
		"empty path pattern":      `{"name":"a","repository_filter":"app","path_pattern":"","actions":["read"]}`,
		"missing path pattern":    `{"name":"a","repository_filter":"app","actions":["read"]}`,
		"empty repo filter":       `{"name":"a","repository_filter":"","path_pattern":"*","actions":["read"]}`,
		"whitespace pattern":      `{"name":"a","repository_filter":"  ","path_pattern":"*","actions":["read"]}`,
		"missing name":            `{"repository_filter":"app","path_pattern":"*","actions":["read"]}`,
		"empty actions":           `{"name":"a","repository_filter":"app","path_pattern":"*","actions":[]}`,
		"missing actions":         `{"name":"a","repository_filter":"app","path_pattern":"*"}`,
		"unknown action":          `{"name":"a","repository_filter":"app","path_pattern":"*","actions":["root"]}`,
		"invalid repo regex":      `{"name":"a","repository_filter":"(","path_pattern":"*","actions":["read"]}`,
		"invalid path regex":      `{"name":"a","repository_filter":"app","path_pattern":"[","actions":["read"]}`,
		"only name and actions":   `{"name":"a","actions":["read","write","delete","manage"]}`,
		"name too long":           `{"name":"` + strings.Repeat("n", 200) + `","repository_filter":"app","path_pattern":"*","actions":["read"]}`,
		"duplicate of seed admin": `{"name":"admin-all","repository_filter":"*","path_pattern":"*","actions":["read"]}`,
	}

	for name, body := range bad {
		rw := doJSON(router, "POST", "/api/v1/permission-targets", body, nil)
		if rw.Code != http.StatusBadRequest && rw.Code != http.StatusConflict {
			t.Errorf("%s: expected 400/409, got %d (%s)", name, rw.Code, rw.Body.String())
		}
	}

	rw := doJSON(router, "POST", "/api/v1/permission-targets",
		`{"name":"ok","repository_filter":"*","path_pattern":"*","actions":["read","annotate"]}`, nil)
	if rw.Code != http.StatusCreated {
		t.Fatalf("valid target: expected 201, got %d (%s)", rw.Code, rw.Body.String())
	}
}

func TestAdminPermissions_PatchValidatesMergedResult(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	rw := doJSON(router, "POST", "/api/v1/permission-targets",
		`{"name":"merge-me","repository_filter":"app","path_pattern":"*","actions":["read"]}`, nil)
	if rw.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rw.Code, rw.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for name, body := range map[string]string{
		"blank path pattern": `{"path_pattern":""}`,
		"blank repo filter":  `{"repository_filter":"   "}`,
		"empty actions":      `{"actions":[]}`,
		"bad regex":          `{"path_pattern":"("}`,
		"blank name":         `{"name":""}`,
		"unknown action":     `{"actions":["read","nuke"]}`,
	} {
		rw = doJSON(router, "PATCH", "/api/v1/permission-targets/"+created.ID, body, nil)
		if rw.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d (%s)", name, rw.Code, rw.Body.String())
		}
	}

	// The stored target must be unchanged after every rejected patch.
	stored, _ := db.GetPermissionTargetByID(context.Background(), created.ID)
	if stored.PathPattern != "*" || stored.RepositoryFilter != "app" || len(stored.Actions) != 1 || stored.Name != "merge-me" {
		t.Errorf("rejected PATCH mutated the stored target: %+v", stored)
	}

	rw = doJSON(router, "PATCH", "/api/v1/permission-targets/"+created.ID, `{"path_pattern":"releases/.*"}`, nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("valid patch: expected 200, got %d (%s)", rw.Code, rw.Body.String())
	}
}

func TestAdminPermissions_AssignChecksExistence(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupPermissionsRouter(t, db)

	rw := doJSON(router, "POST", "/api/v1/permission-targets/981948bd-de8f-5c65-b90b-23d72cf37aab/assignments",
		`{"principal_type":"user","principal_id":"fce32c79-9d88-5887-85ae-70cbd3c8ec02"}`, nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("unknown user: expected 404, got %d", rw.Code)
	}

	rw = doJSON(router, "POST", "/api/v1/permission-targets/981948bd-de8f-5c65-b90b-23d72cf37aab/assignments",
		`{"principal_type":"group","principal_id":"d3c7b2ac-a1d3-516f-ba44-f5aabc17d5a2"}`, nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("unknown group: expected 404, got %d", rw.Code)
	}

	rw = doJSON(router, "POST", "/api/v1/permission-targets/790201a0-c7e6-58d2-9cf7-4802241e1e6d/assignments",
		`{"principal_type":"user","principal_id":"92c8b2f5-9766-576b-931c-5213c062f3aa"}`, nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("unknown target: expected 404, got %d", rw.Code)
	}

	rw = doJSON(router, "POST", "/api/v1/permission-targets/981948bd-de8f-5c65-b90b-23d72cf37aab/assignments",
		`{"principal_type":"robot","principal_id":"92c8b2f5-9766-576b-931c-5213c062f3aa"}`, nil)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("bad principal type: expected 400, got %d", rw.Code)
	}
}

// --- #8 change-password session handling + #15 token principals --------------

func TestHandlerChangePassword_RevokesOtherSessionsKeepsCurrent(t *testing.T) {
	db := newMockDatabase()
	user := createLoginUser(t, db, "cprevoke", "old-password-123", false)

	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	router := chi.NewRouter()
	api.NewAuthHandler(db, log, eval, nil).RegisterAuthed(router)

	current := seedSession(t, db, user.ID)
	seedSession(t, db, user.ID)
	seedSession(t, db, user.ID)

	// A different user's session must be left alone.
	other := createLoginUser(t, db, "cpother", "other-password-123", false)
	seedSession(t, db, other.ID)

	uid := user.ID
	principal := &auth.Principal{UserID: &uid, Username: user.Username, Credential: auth.CredentialSession}

	body, _ := json.Marshal(map[string]string{
		"current_password": "old-password-123",
		"new_password":     "new-password-xyz-456",
	})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: current})
	req = req.WithContext(middleware.WithPrincipal(req.Context(), principal))

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rw.Code, rw.Body.String())
	}

	if n := countUserSessions(db, user.ID); n != 1 {
		t.Fatalf("expected only the caller's session to survive, have %d", n)
	}

	if s, err := auth.ValidateSession(context.Background(), db, current); err != nil || s == nil {
		t.Errorf("the caller's own session must survive (session=%v err=%v)", s, err)
	}

	if n := countUserSessions(db, other.ID); n != 1 {
		t.Errorf("another user's session must not be touched, have %d", n)
	}
}

func TestHandlerChangePassword_PolicyAndTokenPrincipal(t *testing.T) {
	db := newMockDatabase()
	user := createLoginUser(t, db, "cppolicy", "old-password-123", false)

	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	router := chi.NewRouter()
	api.NewAuthHandler(db, log, eval, nil).RegisterAuthed(router)

	uid := user.ID
	session := &auth.Principal{UserID: &uid, Username: user.Username, Credential: auth.CredentialSession}

	rw := doJSON(router, "POST", "/api/v1/auth/change-password",
		`{"current_password":"old-password-123","new_password":"short"}`, session)
	if rw.Code != http.StatusBadRequest || errorCodeOf(t, rw) != "invalid_request" {
		t.Errorf("short new password: expected 400 invalid_request, got %d %s", rw.Code, rw.Body.String())
	}

	tokenP := *session
	tokenP.TokenKind = auth.TokenKindAccess
	tokenP.Credential = auth.CredentialBearerToken

	rw = doJSON(router, "POST", "/api/v1/auth/change-password",
		`{"current_password":"old-password-123","new_password":"new-password-xyz-456"}`, &tokenP)
	if rw.Code != http.StatusForbidden {
		t.Errorf("token principal changing password: expected 403, got %d", rw.Code)
	}

	updated, _ := db.GetUserByID(context.Background(), user.ID)
	if ok, _ := auth.VerifyPassword("old-password-123", *updated.PasswordHash); !ok {
		t.Error("password must be unchanged after a rejected change")
	}
}

func TestSelfTokens_TokenPrincipalCannotMint(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSelfTokensRouter(t, db)

	tokenP := *principal
	tokenP.TokenKind = auth.TokenKindAccess
	tokenP.Credential = auth.CredentialBearerToken

	rw := doJSON(router, "POST", "/api/v1/auth/tokens", `{"name":"child","kind":"access"}`, &tokenP)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("token minting a token: expected 403, got %d: %s", rw.Code, rw.Body.String())
	}

	if len(db.authTokensByID) != 0 {
		t.Error("no token may be created")
	}

	// The same user in an interactive session can.
	rw = doJSON(router, "POST", "/api/v1/auth/tokens", `{"name":"child","kind":"access"}`, principal)
	if rw.Code != http.StatusCreated {
		t.Fatalf("session minting a token: expected 201, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminTokens_TokenPrincipalCannotMintOrManageOwnerTokens(t *testing.T) {
	db := newMockDatabase()
	router, adminP := setupAdminTokensRouter(t, db)
	user, userP := plainUser(t, db, "3faaa366-b247-5b93-9e09-9a11b6012ce5", "ownerone")

	tokenP := *userP
	tokenP.TokenKind = auth.TokenKindDeploy
	tokenP.Credential = auth.CredentialBearerToken

	rw := doJSON(router, "POST", "/api/v1/users/"+user.ID+"/tokens", `{"name":"child","kind":"access"}`, &tokenP)
	if rw.Code != http.StatusForbidden {
		t.Errorf("token principal minting via /users/{id}/tokens: expected 403, got %d", rw.Code)
	}

	rw = doJSON(router, "GET", "/api/v1/users/"+user.ID+"/tokens", "", &tokenP)
	if rw.Code != http.StatusForbidden {
		t.Errorf("token principal listing owner tokens: expected 403, got %d", rw.Code)
	}

	// An admin token principal cannot mint either.
	adminTok := *adminP
	adminTok.TokenKind = auth.TokenKindAccess
	adminTok.Credential = auth.CredentialBearerToken

	rw = doJSON(router, "POST", "/api/v1/users/"+user.ID+"/tokens", `{"name":"child","kind":"access"}`, &adminTok)
	if rw.Code != http.StatusForbidden {
		t.Errorf("admin token minting: expected 403, got %d", rw.Code)
	}
}

func TestAdminTokens_OwnershipChecks(t *testing.T) {
	db := newMockDatabase()
	router, adminP := setupAdminTokensRouter(t, db)
	alice, aliceP := plainUser(t, db, "d01c2c10-2502-5f56-9cee-04175514bdbe", "alice")
	bob, bobP := plainUser(t, db, "b74083aa-6c16-5dd7-ac67-a7c548675d57", "bob")

	rw := doJSON(router, "POST", "/api/v1/users/"+alice.ID+"/tokens", `{"name":"mine","kind":"access"}`, aliceP)
	if rw.Code != http.StatusCreated {
		t.Fatalf("owner create: expected 201, got %d: %s", rw.Code, rw.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if rw = doJSON(router, "POST", "/api/v1/users/"+alice.ID+"/tokens", `{"name":"steal","kind":"access"}`, bobP); rw.Code != http.StatusForbidden {
		t.Errorf("minting for another user as non-admin: expected 403, got %d", rw.Code)
	}

	if rw = doJSON(router, "GET", "/api/v1/users/"+alice.ID+"/tokens", "", bobP); rw.Code != http.StatusForbidden {
		t.Errorf("listing another user's tokens: expected 403, got %d", rw.Code)
	}

	if rw = doJSON(router, "DELETE", "/api/v1/users/"+alice.ID+"/tokens/"+created.ID, "", bobP); rw.Code != http.StatusForbidden {
		t.Errorf("revoking another user's token: expected 403, got %d", rw.Code)
	}

	if rw = doJSON(router, "DELETE", "/api/v1/users/"+bob.ID+"/tokens/"+created.ID, "", bobP); rw.Code != http.StatusForbidden {
		t.Errorf("revoking alice's token through bob's path: expected 403, got %d", rw.Code)
	}

	if rw = doJSON(router, "GET", "/api/v1/users/"+alice.ID+"/tokens", "", adminP); rw.Code != http.StatusOK {
		t.Errorf("admin listing: expected 200, got %d", rw.Code)
	}
}

func TestTokens_ScopeAndInputValidation(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupSelfTokensRouter(t, db)

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	far := time.Now().Add(10 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	cases := map[string]struct {
		body string
		want int
	}{
		"scope not held":       {`{"name":"t","kind":"access","scopes":["981948bd-de8f-5c65-b90b-23d72cf37aab"]}`, http.StatusForbidden},
		"empty name":           {`{"name":"  ","kind":"access"}`, http.StatusBadRequest},
		"long name":            {`{"name":"` + strings.Repeat("x", 101) + `","kind":"access"}`, http.StatusBadRequest},
		"bad kind":             {`{"name":"t","kind":"root"}`, http.StatusBadRequest},
		"expiry in past":       {fmt.Sprintf(`{"name":"t","kind":"access","expires_at":%q}`, past), http.StatusBadRequest},
		"expiry beyond max":    {fmt.Sprintf(`{"name":"t","kind":"access","expires_at":%q}`, far), http.StatusBadRequest},
		"empty scope id":       {`{"name":"t","kind":"access","scopes":[""]}`, http.StatusBadRequest},
		"valid with expiry":    {fmt.Sprintf(`{"name":"t","kind":"deploy","expires_at":%q}`, future), http.StatusCreated},
		"valid no scopes/none": {`{"name":"t2","kind":"access"}`, http.StatusCreated},
	}

	for name, tc := range cases {
		rw := doJSON(router, "POST", "/api/v1/auth/tokens", tc.body, principal)
		if rw.Code != tc.want {
			t.Errorf("%s: expected %d, got %d (%s)", name, tc.want, rw.Code, rw.Body.String())
		}
	}

	// A held target is an acceptable scope.
	if err := db.CreatePermissionAssignment(context.Background(), &models.PermissionAssignment{
		PermissionTargetID: "981948bd-de8f-5c65-b90b-23d72cf37aab",
		PrincipalType:      "user",
		PrincipalID:        *principal.UserID,
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	rw := doJSON(router, "POST", "/api/v1/auth/tokens", `{"name":"scoped","kind":"access","scopes":["981948bd-de8f-5c65-b90b-23d72cf37aab"]}`, principal)
	if rw.Code != http.StatusCreated {
		t.Errorf("held scope: expected 201, got %d (%s)", rw.Code, rw.Body.String())
	}

	// Admins may only name scopes that exist.
	adminDB := newMockDatabase()
	adminRouter, adminP := setupAdminTokensRouter(t, adminDB)

	rw = doJSON(adminRouter, "POST", "/api/v1/users/"+*adminP.UserID+"/tokens", `{"name":"t","kind":"access","scopes":["790201a0-c7e6-58d2-9cf7-4802241e1e6d"]}`, adminP)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("admin unknown scope: expected 400, got %d (%s)", rw.Code, rw.Body.String())
	}
}

// --- #13 login throttling -----------------------------------------------------

func loginFrom(router http.Handler, remoteAddr, username, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

// loginDistributedLimit mirrors the handler's per-username failure budget
// across all IPs.
const loginDistributedLimit = 100

// An attacker burning the per-(username, IP) budget locks out only their own
// address; the real user logging in from another IP is unaffected.
func TestHandlerLogin_PairLockoutDoesNotBlockOtherIP(t *testing.T) {
	db := newMockDatabase()
	createLoginUser(t, db, "victim", "the-real-password-1", false)

	router, _ := setupHandlerRouter(t, db)

	for range 10 {
		loginFrom(router, "198.51.100.7:5000", "victim", "wrong-guess")
	}

	if rw := loginFrom(router, "198.51.100.7:5000", "victim", "the-real-password-1"); rw.Code != http.StatusTooManyRequests {
		t.Fatalf("attacker IP must be locked for that username, got %d", rw.Code)
	}

	if rw := loginFrom(router, "192.0.2.77:5000", "victim", "the-real-password-1"); rw.Code != http.StatusOK {
		t.Fatalf("legitimate login from another IP must succeed, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestHandlerLogin_PerUsernameFailureLimit(t *testing.T) {
	db := newMockDatabase()
	createLoginUser(t, db, "bruteforced", "the-real-password-1", false)
	createLoginUser(t, db, "bystander", "bystander-password-1", false)

	router, _ := setupHandlerRouter(t, db)

	// Distributed attack: every guess comes from a different IP, so the
	// per-IP limiter never trips; only the per-username limiter can stop it.
	for i := range loginDistributedLimit {
		rw := loginFrom(router, fmt.Sprintf("198.51.%d.%d:5000", 100+i/250, i%250+1), "bruteforced", "wrong-guess")
		if rw.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rw.Code)
		}
	}

	rw := loginFrom(router, "198.51.100.200:5000", "bruteforced", "the-real-password-1")
	if rw.Code != http.StatusTooManyRequests {
		t.Fatalf("locked username must get 429 even with the right password, got %d", rw.Code)
	}

	if rw.Header().Get("Retry-After") == "" {
		t.Error("429 must carry Retry-After")
	}

	// Case-insensitive: the lock cannot be dodged by changing the case.
	if rw = loginFrom(router, "198.51.100.201:5000", "BruteForced", "the-real-password-1"); rw.Code != http.StatusTooManyRequests {
		t.Errorf("username case variant must share the lock, got %d", rw.Code)
	}

	// Other accounts are unaffected.
	if rw = loginFrom(router, "198.51.100.202:5000", "bystander", "bystander-password-1"); rw.Code != http.StatusOK {
		t.Errorf("unrelated username must still log in, got %d: %s", rw.Code, rw.Body.String())
	}

	// A username that does not exist is throttled identically (no oracle).
	for i := range loginDistributedLimit {
		loginFrom(router, fmt.Sprintf("203.0.%d.%d:5000", i/250, i%250+1), "ghost-account", "guess")
	}

	if rw = loginFrom(router, "203.0.113.250:5000", "ghost-account", "guess"); rw.Code != http.StatusTooManyRequests {
		t.Errorf("unknown username must be throttled like a real one, got %d", rw.Code)
	}
}

func TestHandlerLogin_SuccessResetsUsernameFailures(t *testing.T) {
	db := newMockDatabase()
	createLoginUser(t, db, "resetme", "the-real-password-1", false)

	router, _ := setupHandlerRouter(t, db)

	for round := range 3 {
		for i := range 9 {
			addr := fmt.Sprintf("192.0.2.%d:%d", 10+round*10+i, 6000)
			if rw := loginFrom(router, addr, "resetme", "wrong-guess"); rw.Code != http.StatusUnauthorized {
				t.Fatalf("round %d attempt %d: expected 401, got %d", round, i, rw.Code)
			}
		}

		addr := fmt.Sprintf("192.0.2.%d:%d", 100+round, 6000)
		if rw := loginFrom(router, addr, "resetme", "the-real-password-1"); rw.Code != http.StatusOK {
			t.Fatalf("round %d: legitimate login after 9 failures must succeed, got %d", round, rw.Code)
		}
	}
}

func TestHandlerLogin_PerIPLimit(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupHandlerRouter(t, db)

	var last int

	for i := range 12 {
		// Rotate usernames so only the IP limiter can fire.
		rw := loginFrom(router, "192.0.2.77:4000", fmt.Sprintf("nobody%d", i), "x")
		last = rw.Code
	}

	if last != http.StatusTooManyRequests {
		t.Errorf("expected the per-IP limiter to answer 429 after the budget, got %d", last)
	}
}

func TestHandlerLogin_SessionCookieAttributes(t *testing.T) {
	db := newMockDatabase()
	createLoginUser(t, db, "cookieuser", "the-real-password-1", false)

	router, _ := setupHandlerRouter(t, db)

	body := `{"username":"cookieuser","password":"the-real-password-1"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rw.Code, rw.Body.String())
	}

	var session *http.Cookie

	for _, c := range rw.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			session = c
		}
	}

	if session == nil {
		t.Fatal("no session cookie")
	}

	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode || session.Path != "/" {
		t.Errorf("session cookie attributes wrong: %+v", session)
	}

	// Logout must clear it with the same attributes.
	req = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/logout", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(session)

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	for _, c := range rw.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			if c.MaxAge >= 0 || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("logout cookie attributes wrong: %+v", c)
			}

			return
		}
	}

	t.Error("logout must expire the session cookie")
}
