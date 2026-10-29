// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// userPatchMockDBStore is what userPatchMockDB is used as.
type userPatchMockDBStore interface {
	AdminUsersStore
	auth.EvaluatorStore
}

// userPatchMockDB holds one user and records what UpdateUser stores.
type userPatchMockDB struct {
	userPatchMockDBStore

	user    models.User
	updated *models.User
}

func (db *userPatchMockDB) GetUserByID(context.Context, string) (*models.User, error) {
	u := db.user

	return &u, nil
}

func (db *userPatchMockDB) UpdateUser(_ context.Context, u *models.User) error {
	db.updated = u
	return nil
}

// PATCH {"email": ""} clears the address (NULL) instead of storing "": the
// unique email index would otherwise reject the second user that clears theirs
// with a misleading 409.
func TestAdminUsersPatchEmptyEmailClearsIt(t *testing.T) {
	const id = "6f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11"

	email := "old@example.com"

	for _, tc := range []struct {
		name  string
		body  string
		email *string
	}{
		{"empty clears", `{"email":""}`, nil},
		{"address is stored", `{"email":"new@example.com"}`, new("new@example.com")},
		{"omitted keeps", `{"full_name":"x"}`, &email},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &userPatchMockDB{user: models.User{ID: id, Username: "someone", Email: &email}}
			log := newFixTestLogger(t)
			r := chi.NewRouter()
			NewAdminUsersHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/api/v1/users/"+id, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

			rw := httptest.NewRecorder()
			r.ServeHTTP(rw, req)

			if rw.Code != http.StatusOK {
				t.Fatalf("PATCH = %d, want 200: %s", rw.Code, rw.Body.String())
			}

			if db.updated == nil {
				t.Fatal("UpdateUser was not called")
			}

			switch got := db.updated.Email; {
			case tc.email == nil && got != nil:
				t.Errorf("stored email = %q, want NULL", *got)
			case tc.email != nil && (got == nil || *got != *tc.email):
				t.Errorf("stored email = %v, want %q", got, *tc.email)
			}
		})
	}
}
