// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// tokenLookupMockDB looks tokens up like the uuid-keyed table: an id that is
// not a UUID is an error, an unknown UUID is "no such token".
type tokenLookupMockDB struct {
	databasetest.BaseMockDB
}

func (db *tokenLookupMockDB) GetTokenByID(_ context.Context, id string) (*models.Token, error) {
	if !isUUIDString(id) {
		return nil, errors.New("invalid input syntax for type uuid: " + id)
	}

	return nil, nil
}

// A token id that is not a UUID is a 404 on the admin revoke route, not a
// database error; an unknown UUID is a 404 as well.
func TestAdminRevokeTokenUnknownIDIsNotFound(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	db := &tokenLookupMockDB{}
	r := chi.NewRouter()
	NewAdminTokensHandler(db, log, auth.NewEvaluator(db, log, false)).Register(r)

	for _, id := range []string{"0", "not-a-uuid", "null%2Cnull", "6f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/admin/tokens/"+id, nil)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

		rw := httptest.NewRecorder()
		r.ServeHTTP(rw, req)

		if rw.Code != http.StatusNotFound {
			t.Errorf("DELETE /api/v1/admin/tokens/%s = %d, want 404: %s", id, rw.Code, rw.Body.String())
		}
	}
}
