// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errDBDown = errors.New("failed to get token by hash: failed to connect to host=db.internal user=oa: connection refused")

// flakyRBACDB is a benchRBACDB whose token and repository lookups can fail.
type flakyRBACDB struct {
	benchRBACDB

	tokenErr error
	repoErr  error
}

func (d *flakyRBACDB) GetTokenByHash(ctx context.Context, hash string) (*models.Token, error) {
	if d.tokenErr != nil {
		return nil, d.tokenErr
	}

	return d.benchRBACDB.GetTokenByHash(ctx, hash)
}

func (d *flakyRBACDB) GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error) {
	if d.repoErr != nil {
		return nil, d.repoErr
	}

	return d.benchRBACDB.GetRepositoryByName(ctx, name)
}

// authOutcome runs req through RBACMiddleware over db and returns the recorder
// and whether the downstream handler ran.
func authOutcome(t *testing.T, db *flakyRBACDB, req *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true

		w.WriteHeader(http.StatusOK)
	})
	handler := RBACMiddleware(RBACConfig{MCPPath: "/mcp"}, db, auth.NewEvaluator(db, log, false), log)(next)

	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	return rw, reached
}

func newFlakyDB() *flakyRBACDB {
	db := &flakyRBACDB{}
	db.repos = map[string]*models.Repository{"pub": {Name: "pub", Public: true}}
	db.tokens = map[string]*models.Token{}
	db.users = map[string]*models.User{}
	db.sessions = map[string]*models.Session{}

	return db
}

func assertUnavailable(t *testing.T, rw *httptest.ResponseRecorder, reached bool) {
	t.Helper()

	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %q)", rw.Code, rw.Body.String())
	}

	if reached {
		t.Fatal("the handler ran for a request whose authentication could not be decided")
	}

	if rw.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}

	if body := rw.Body.String(); strings.Contains(body, "connect") || !strings.Contains(body, "service_unavailable") {
		t.Errorf("body = %q, want the service_unavailable envelope without the database error", body)
	}
}

// TestRBACTokenLookupFailureIs503 pins that a database failure while resolving a
// token is a 503, never the 401 reserved for a token that is not valid, for
// every credential shape and for the MCP endpoint.
func TestRBACTokenLookupFailureIs503(t *testing.T) {
	token := auth.TokenPrefixAccess + "abc"

	tests := []struct {
		name   string
		path   string
		header func(*http.Request)
	}{
		{"bearer", "/repos/pub/pkg", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }},
		{"basic", "/repos/pub/pkg", func(r *http.Request) { r.SetBasicAuth("jane", token) }},
		{"mcp", "/mcp", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newFlakyDB()
			db.tokenErr = errDBDown

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, http.NoBody)
			tc.header(req)

			rw, reached := authOutcome(t, db, req)
			assertUnavailable(t, rw, reached)
		})
	}
}

// TestRBACUnknownTokenIs401 pins the other half: a token the database does not
// know is unauthenticated (401 on a private repository), not unavailable.
func TestRBACUnknownTokenIs401(t *testing.T) {
	db := newFlakyDB()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/private/pkg", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+auth.TokenPrefixAccess+"unknown")

	rw, reached := authOutcome(t, db, req)

	if rw.Code != http.StatusUnauthorized || reached {
		t.Fatalf("status = %d reached = %v, want 401 and a refused request (body %q)", rw.Code, reached, rw.Body.String())
	}
}

// TestRBACRepoLookupFailureIs503 pins that a database failure during the
// authorization of an anonymous read (the repository lookup behind the public
// flag) is a 503, not a 500.
func TestRBACRepoLookupFailureIs503(t *testing.T) {
	db := newFlakyDB()
	db.repoErr = errDBDown

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/pub/pkg", http.NoBody)

	rw, reached := authOutcome(t, db, req)
	assertUnavailable(t, rw, reached)
}

// TestRBACAbandonedRequestIsNot503 pins that a client that hung up mid-lookup
// is not counted as an outage.
func TestRBACAbandonedRequestIsNot503(t *testing.T) {
	db := newFlakyDB()
	db.tokenErr = context.Canceled

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/repos/pub/pkg", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+auth.TokenPrefixAccess+"abc")

	rw, reached := authOutcome(t, db, req)

	if rw.Code != statusClientClosedRequest || reached {
		t.Fatalf("status = %d reached = %v, want %d and a refused request", rw.Code, reached, statusClientClosedRequest)
	}
}
