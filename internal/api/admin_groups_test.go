// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// groupMembersMockDB is an empty group whose membership insert fails like
// auth_user_groups does for a user id that does not exist.
type groupMembersMockDB struct {
	databasetest.BaseMockDB
}

func (db *groupMembersMockDB) GetGroupByID(_ context.Context, id string) (*models.Group, error) {
	return &models.Group{ID: id, Name: "g"}, nil
}

func (db *groupMembersMockDB) ListUsersByGroup(context.Context, string) ([]*models.User, error) {
	return nil, nil
}

func (db *groupMembersMockDB) AddUserToGroup(context.Context, string, string) error {
	return fmt.Errorf("failed to add user to group: %w", &pgconn.PgError{Code: "23503"})
}

// Naming a user that does not exist in a group's member list is a 400, not a
// database error.
func TestSetGroupMembersUnknownUserIsBadRequest(t *testing.T) {
	log := newFixTestLogger(t)
	db := &groupMembersMockDB{}
	r := chi.NewRouter()
	NewAdminGroupsHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

	body := `{"members":["d947c217-221c-3f9d-8a88-2bf7de623c0c"]}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut,
		"/api/v1/groups/6f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11/members", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("PUT members = %d, want 400: %s", rw.Code, rw.Body.String())
	}
}
