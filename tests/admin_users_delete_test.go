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
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

func doUsersReq(t *testing.T, router chi.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}

	req := httptest.NewRequestWithContext(context.Background(), method, path, rdr)
	req.Header.Set("Content-Type", "application/json")

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func seedUser(t *testing.T, db *MockDatabase, name string) *models.User {
	t.Helper()

	u, err := db.CreateUser(context.Background(), &models.User{Username: name, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := db.CreateSession(context.Background(), &models.Session{UserID: u.ID, TokenHash: "sess-" + name}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return u
}

func TestAdminUsers_DeleteRemovesUserTokensAndSessions(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)
	u := seedUser(t, db, "victim")

	if _, err := db.CreateToken(context.Background(), &models.Token{UserID: u.ID, Name: "t", TokenHash: "h1"}); err != nil {
		t.Fatal(err)
	}

	if rw := doUsersReq(t, router, "DELETE", "/api/v1/users/"+u.ID, ""); rw.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d: %s", rw.Code, rw.Body.String())
	}

	if got, _ := db.GetUserByID(context.Background(), u.ID); got != nil {
		t.Error("user row still present after delete")
	}

	if _, ok := db.authSessions["sess-victim"]; ok {
		t.Error("session survived delete")
	}

	if toks, _ := db.ListTokensByUser(context.Background(), u.ID); len(toks) != 0 {
		t.Errorf("tokens survived delete: %d", len(toks))
	}

	if rw := doUsersReq(t, router, "GET", "/api/v1/users/"+u.ID, ""); rw.Code != http.StatusNotFound {
		t.Errorf("get after delete: want 404, got %d", rw.Code)
	}
}

func TestAdminUsers_DeleteGuards(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)

	if rw := doUsersReq(t, router, "DELETE", "/api/v1/users/36d86ead-884f-5ded-9408-ffc1c86bdbfa", ""); rw.Code != http.StatusBadRequest {
		t.Errorf("self delete: want 400, got %d", rw.Code)
	}

	svcProject := uuid.NewString()
	svc, _ := db.CreateUser(context.Background(), &models.User{Username: "svc-x", Kind: models.UserKindService, ProjectID: &svcProject})

	if rw := doUsersReq(t, router, "DELETE", "/api/v1/users/"+svc.ID, ""); rw.Code != http.StatusNotFound {
		t.Errorf("service delete: want 404, got %d", rw.Code)
	}

	last := seedUser(t, db, "lastadmin")
	db.lastAdminID = last.ID

	if rw := doUsersReq(t, router, "DELETE", "/api/v1/users/"+last.ID, ""); rw.Code != http.StatusConflict {
		t.Errorf("last admin delete: want 409, got %d", rw.Code)
	}

	if got, _ := db.GetUserByID(context.Background(), last.ID); got == nil {
		t.Error("last admin was deleted")
	}

	if rw := doUsersReq(t, router, "DELETE", "/api/v1/users/"+uuid.NewString(), ""); rw.Code != http.StatusNotFound {
		t.Errorf("unknown user: want 404, got %d", rw.Code)
	}
}

func TestAdminUsers_DisableKeepsRowAndRevokesSessions(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)
	u := seedUser(t, db, "sleeper")

	if rw := doUsersReq(t, router, "PATCH", "/api/v1/users/"+u.ID, `{"disabled":true}`); rw.Code != http.StatusOK {
		t.Fatalf("disable: want 200, got %d: %s", rw.Code, rw.Body.String())
	}

	got, _ := db.GetUserByID(context.Background(), u.ID)
	if got == nil || !got.Disabled {
		t.Fatalf("user must remain and be disabled, got %+v", got)
	}

	if _, ok := db.authSessions["sess-sleeper"]; ok {
		t.Error("session survived disable")
	}

	if rw := doUsersReq(t, router, "PATCH", "/api/v1/users/"+u.ID, `{"disabled":false}`); rw.Code != http.StatusOK {
		t.Fatalf("enable: want 200, got %d", rw.Code)
	}
}

func TestAdminTokens_UserListExcludesRevoked(t *testing.T) {
	db := newMockDatabase()
	router, _ := setupUsersRouter(t, db)
	log := newHandlerLogger(t)
	api.NewAdminTokensHandler(db, log, auth.NewEvaluator(db, log, false)).Register(router)

	u := seedUser(t, db, "tokowner")
	now := time.Now()

	for _, tk := range []*models.Token{
		{UserID: u.ID, Name: "live", TokenHash: "a"},
		{UserID: u.ID, Name: "dead", TokenHash: "b", RevokedAt: &now},
	} {
		if _, err := db.CreateToken(context.Background(), tk); err != nil {
			t.Fatal(err)
		}
	}

	rw := doUsersReq(t, router, "GET", "/api/v1/users/"+u.ID+"/tokens", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("list: %d", rw.Code)
	}

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(rw.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if len(resp.Data) != 1 || resp.Data[0]["name"] != "live" {
		t.Errorf("want only the live token, got %v", resp.Data)
	}
}
