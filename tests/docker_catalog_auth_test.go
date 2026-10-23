// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
)

// GET /v2/_catalog names no repository: any authenticated principal reaches
// the handler, which lists only what the principal may read, and anonymous
// callers are held to the same rule as the /v2/ ping. Everything else under
// /v2/ is still evaluated per repository.
func TestRBAC_DockerCatalogAllowsAnyAuthenticatedPrincipal(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "scoped-catalog-user")
	grantTarget(t, db, user.ID, "team-only", "^team-.*$", ".*", "read")

	tok, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "ci", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	if rw := serve(handler, http.MethodGet, "/v2/_catalog", bearerHeader(tok)); rw.Code != http.StatusOK {
		t.Errorf("authenticated GET /v2/_catalog = %d, want 200", rw.Code)
	}

	if rw := serve(handler, http.MethodGet, "/v2/_catalog", nil); rw.Code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /v2/_catalog = %d, want 401", rw.Code)
	}

	if rw := serve(handler, http.MethodPost, "/v2/_catalog", bearerHeader(tok)); rw.Code == http.StatusOK {
		t.Errorf("POST /v2/_catalog = 200, want it evaluated like any write")
	}

	if rw := serve(handler, http.MethodGet, "/v2/_catalog/manifests/latest", bearerHeader(tok)); rw.Code == http.StatusOK {
		t.Errorf("a repository named _catalog must stay subject to its permissions")
	}
}
