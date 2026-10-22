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

// FMTA/AUTH-2: GET /v2/ names no repository, so any authenticated principal
// passes (even one whose targets only match a repo prefix); anonymous does not.
func TestRBAC_DockerPingAllowsAnyAuthenticatedPrincipal(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := newBareUser(t, db, "scoped-user")
	grantTarget(t, db, user.ID, "team-only", "^team-.*$", ".*", "read")

	tok, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "ci", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	handler := newRBACMiddleware(db, middleware.RBACConfig{}, newMWLogger(t))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if rw := serve(handler, method, "/v2/", bearerHeader(tok)); rw.Code != http.StatusOK {
			t.Errorf("authenticated %s /v2/ = %d, want 200", method, rw.Code)
		}
	}

	if rw := serve(handler, http.MethodGet, "/v2/", nil); rw.Code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /v2/ = %d, want 401", rw.Code)
	}

	// The exemption is exactly /v2/: image paths are still evaluated.
	if rw := serve(handler, http.MethodGet, "/v2/other/manifests/latest", bearerHeader(tok)); rw.Code == http.StatusOK {
		t.Errorf("out-of-scope manifest read = 200, want denied")
	}
}
