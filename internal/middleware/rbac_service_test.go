// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// TestRBACProjectTokenServiceAccount drives a project token (deploy token of a
// service account of project acme with role developer) through the real
// middleware: it reaches the repositories of acme and nothing else.
func TestRBACProjectTokenServiceAccount(t *testing.T) {
	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	acme, beta := acmeProjectID, "proj-beta"

	// The service user also holds an admin permission target: it must be ignored.
	db := &projectRBACDB{grants: []*models.ProjectGrant{{ProjectID: acmeProjectID, Role: models.ProjectRoleDeveloper}}}
	db.targets = []*models.PermissionTarget{{ID: "t-admin", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"manage", "read", "write", "delete"}}}
	db.repos = map[string]*models.Repository{
		"acme-npm": {Name: "acme-npm", ProjectID: &acme},
		"beta-npm": {Name: "beta-npm", ProjectID: &beta},
	}
	db.tokens = map[string]*models.Token{}
	db.users = map[string]*models.User{"svc-1": {ID: "svc-1", Username: "svc-acme-1", Kind: models.UserKindService, ProjectID: &acme}}

	secret, tok, err := auth.CreateProjectToken(t.Context(), db, ptSpec(acme))
	if err != nil {
		t.Fatal(err)
	}

	tok.ProjectID = &acme
	db.tokens[tok.TokenHash] = tok

	handler := RBACMiddleware(RBACConfig{ProjectSettings: auth.NewProjectSettings(db, false)}, db, auth.NewEvaluator(db, log, false), log)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodPut, "/repos/acme-npm/pkg/-/pkg-1.0.0.tgz", http.StatusOK},
		{http.MethodGet, "/repos/acme-npm/pkg", http.StatusOK},
		{http.MethodDelete, "/repos/acme-npm/pkg", http.StatusForbidden},
		{http.MethodPut, "/repos/beta-npm/pkg/-/pkg-1.0.0.tgz", http.StatusForbidden},
		{http.MethodGet, "/repos/beta-npm/pkg", http.StatusForbidden},
		{http.MethodGet, "/api/v1/auth/me", http.StatusForbidden},
		{http.MethodGet, "/api/v1/projects", http.StatusForbidden},
		{http.MethodGet, "/api/v1/projects/acme", http.StatusForbidden},
		{http.MethodPost, "/api/v1/projects/acme/tokens", http.StatusForbidden},
		{http.MethodGet, "/api/v1/repositories", http.StatusForbidden},
		{http.MethodPost, "/api/v1/auth/tokens", http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+secret)

			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, req)

			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d", rw.Code, tc.want)
			}
		})
	}

	t.Run("revoked token is refused", func(t *testing.T) {
		revoked := *tok
		now := tok.CreatedAt
		revoked.RevokedAt = &now
		db.tokens[tok.TokenHash] = &revoked

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/acme-npm/pkg", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+secret)

		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)

		if rw.Code == http.StatusOK {
			t.Fatal("a revoked project token was accepted")
		}
	})
}
