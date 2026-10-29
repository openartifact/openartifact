// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// groupMembersMockDBStore is what groupMembersMockDB is used as.
type groupMembersMockDBStore interface {
	AdminGroupsStore
	auth.EvaluatorStore
}

// groupMembersMockDB is an empty group whose membership replacement fails like
// auth_user_groups does for a user id that does not exist.
type groupMembersMockDB struct {
	groupMembersMockDBStore
}

func (db *groupMembersMockDB) GetGroupByID(_ context.Context, id string) (*models.Group, error) {
	return &models.Group{ID: id, Name: "g"}, nil
}

func (db *groupMembersMockDB) SetGroupMembers(context.Context, string, []string) error {
	return fmt.Errorf("failed to add group member: %w", &pgconn.PgError{Code: "23503"})
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

// groupDeleteMockDB holds one group and records which ids were deleted.
type groupDeleteMockDB struct {
	groupMembersMockDBStore

	known   string
	deleted []string
}

func (db *groupDeleteMockDB) GetGroupByID(_ context.Context, id string) (*models.Group, error) {
	if id != db.known {
		return nil, nil
	}

	return &models.Group{ID: id, Name: "g"}, nil
}

func (db *groupDeleteMockDB) DeleteGroup(_ context.Context, id string) error {
	db.deleted = append(db.deleted, id)
	return nil
}

// Deleting a group that does not exist is a 404 and leaves no audit row; it must
// not be reported as a deletion. Deleting an existing one is a 204 and one row.
func TestDeleteGroupMissingIsNotFoundAndNotAudited(t *testing.T) {
	const (
		known   = "6f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11"
		missing = "d947c217-221c-3f9d-8a88-2bf7de623c0c"
	)

	for _, tc := range []struct {
		name        string
		id          string
		status      int
		wantDeleted []string
		wantAudit   []string
	}{
		{"missing", missing, http.StatusNotFound, nil, nil},
		{"existing", known, http.StatusNoContent, []string{known}, []string{audit.ActionGroupDeleted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := recordAudit(t)
			db := &groupDeleteMockDB{known: known}
			log := newFixTestLogger(t)
			r := chi.NewRouter()
			NewAdminGroupsHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/groups/"+tc.id, nil)
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

			rw := httptest.NewRecorder()
			r.ServeHTTP(rw, req)

			if rw.Code != tc.status {
				t.Fatalf("DELETE = %d, want %d: %s", rw.Code, tc.status, rw.Body.String())
			}

			if !slices.Equal(db.deleted, tc.wantDeleted) {
				t.Errorf("deleted = %v, want %v", db.deleted, tc.wantDeleted)
			}

			if got := auditActions(rows()); !slices.Equal(got, tc.wantAudit) {
				t.Errorf("audit actions = %v, want %v", got, tc.wantAudit)
			}
		})
	}
}
