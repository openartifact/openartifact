// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// catchAllRBAC serves RBACMiddleware over a user whose permission target
// matches every repository, path and action, and the bearer token of that user.
// next answers 200 and reports the repository the chi router routed to.
func catchAllRBAC(t *testing.T, next http.Handler) (http.Handler, string) {
	t.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	db := &benchRBACDB{
		targets: []*models.PermissionTarget{
			{ID: "all", RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read", "write", "delete"}},
		},
		repos:    map[string]*models.Repository{"abc": {Name: "abc"}},
		tokens:   map[string]*models.Token{},
		users:    map[string]*models.User{"user-1": {ID: "user-1", Username: "jane"}},
		sessions: map[string]*models.Session{},
	}

	raw, tok, err := auth.CreateToken(t.Context(), db, auth.TokenKindAccess, "ci", "user-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	tok.LastUsedAt = &now
	db.tokens[tok.TokenHash] = tok

	return RBACMiddleware(RBACConfig{}, db, auth.NewEvaluator(db, log, false), log)(next), raw
}

// A permission target for ".*" matches the empty repository name, so a request
// that names no repository must never be authorized as one: /repos//x is routed
// by chi to a handler with an empty {name}.
func TestRBACRefusesRequestsNamingNoRepository(t *testing.T) {
	var routed bool

	router := chi.NewRouter()
	router.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		routed = true

		w.WriteHeader(http.StatusOK)
	}))

	handler, token := catchAllRBAC(t, router)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/repos//x", http.StatusNotFound},
		{http.MethodPut, "/repos//x", http.StatusNotFound},
		{http.MethodDelete, "/repos//", http.StatusNotFound},
		{http.MethodGet, "/repos/", http.StatusNotFound},
		{http.MethodGet, "/repos/raw//x", http.StatusNotFound},
		{http.MethodPut, "/v2//manifests/latest", http.StatusNotFound},
		// The registry ping and catalog name no repository by design.
		{http.MethodGet, "/v2/", http.StatusOK},
		{http.MethodGet, "/v2/_catalog", http.StatusOK},
		// A real repository is unaffected.
		{http.MethodGet, "/repos/abc/x", http.StatusOK},
		{http.MethodPut, "/repos/raw/abc/x.txt", http.StatusOK},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			routed = false

			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+token)

			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, req)

			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d", rw.Code, tc.want)
			}

			if routed != (tc.want == http.StatusOK) {
				t.Fatalf("routed = %v with status %d", routed, rw.Code)
			}
		})
	}
}

// RBAC reads the decoded path while chi routes on the escaped one when the
// request has a RawPath. For every URL RBAC lets through, the repository it
// authorized must be the one the router hands the handler, so a percent-escape
// can never make the two name different repositories.
func TestRBACAuthorizedRepositoryIsTheRoutedOne(t *testing.T) {
	var routed string

	record := func(w http.ResponseWriter, r *http.Request) {
		routed = chi.URLParam(r, "name")

		w.WriteHeader(http.StatusOK)
	}

	router := chi.NewRouter()
	router.Route("/repos/raw/{name}", func(r chi.Router) { r.HandleFunc("/*", record) })
	router.Route("/repos/{name}", func(r chi.Router) { r.HandleFunc("/*", record) })

	handler, token := catchAllRBAC(t, router)

	for _, tc := range []struct {
		path string
		repo string // the repository RBAC authorizes; "" = refused
	}{
		{"/repos/abc/x", "abc"},
		{"/repos/abc/a%2Fb", "abc"},
		{"/repos/abc/p%61th", "abc"},
		{"/repos/raw/abc/x.txt", "abc"},
		{"/repos/a%62c/x", ""},
		{"/repos/%61bc/x", ""},
		{"/repos/raw/a%62c/x.txt", ""},
		{"/repos/a%2562c/x", ""},
		{"/repos/abc%2fdef/x", ""},
		{"/repos/%2e/x", ""},
		{"/repos//x", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			routed = ""

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, http.NoBody)
			req.Header.Set("Authorization", "Bearer "+token)

			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, req)

			if tc.repo == "" {
				if rw.Code < http.StatusBadRequest || routed != "" {
					t.Fatalf("status = %d, routed to %q: want a refusal before the router", rw.Code, routed)
				}

				return
			}

			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rw.Code)
			}

			if rbacRepo, _ := extractRepoAndPath(req.URL.Path); rbacRepo != tc.repo || routed != tc.repo {
				t.Fatalf("RBAC authorized %q but the router served %q, want %q", rbacRepo, routed, tc.repo)
			}
		})
	}
}
