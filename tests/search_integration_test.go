// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
)

type searchResponse struct {
	Total        int64                     `json:"total"`
	Packages     []search.SearchResult     `json:"packages"`
	Repositories []search.RepositoryResult `json:"repositories"`
}

func (r searchResponse) repoNames() []string {
	names := make([]string, 0, len(r.Repositories))
	for _, repo := range r.Repositories {
		names = append(names, repo.Name)
	}

	return names
}

func (r searchResponse) packageNames() []string {
	names := make([]string, 0, len(r.Packages))
	for _, pkg := range r.Packages {
		names = append(names, pkg.Repository+":"+pkg.Name)
	}

	return names
}

func newSearchRepo(t *testing.T, pg *database.PostgresDB, name, format, description string, public bool) {
	t.Helper()

	systemOrg := "00000000-0000-0000-0000-000000000001"
	repo := &models.Repository{
		Name: name, Format: format, Description: description, Public: public,
		RepoType: "hosted", ProjectID: &systemOrg,
	}

	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", name, err)
	}
}

// searchUser creates a user holding one read target over repoFilter (none when
// repoFilter is empty) and returns its principal.
func searchUser(t *testing.T, pg *database.PostgresDB, username, repoFilter string) *auth.Principal {
	t.Helper()

	ctx := context.Background()

	user, err := pg.CreateUser(ctx, &models.User{Username: username})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}

	if repoFilter != "" {
		target, err := pg.CreatePermissionTarget(ctx, &models.PermissionTarget{
			Name: username + "-read", RepositoryFilter: repoFilter, PathPattern: ".*", Actions: []string{"read"},
		})
		if err != nil {
			t.Fatalf("CreatePermissionTarget(%s): %v", username, err)
		}

		if err := pg.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
			PermissionTargetID: target.ID, PrincipalType: "user", PrincipalID: user.ID,
		}); err != nil {
			t.Fatalf("CreatePermissionAssignment(%s): %v", username, err)
		}
	}

	return &auth.Principal{UserID: &user.ID, Username: username}
}

// TestSearchFindsRepositoriesByName reproduces "q=ui-kit finds packages named
// @acme/ui-kit but not the repository named ui-kit": repository names must be
// matched by substring (hyphens included), literally, case-insensitively, and
// only for repositories the caller may read.
func TestSearchFindsRepositoriesByName(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	newSearchRepo(t, pg, "ui-kit", "npm", "shared components", false)
	newSearchRepo(t, pg, "ui-kit-docs", "raw", "", false)
	newSearchRepo(t, pg, "my-ui-kit-lib", "helm", "", false)
	newSearchRepo(t, pg, "ui_kit", "pypi", "", false)
	newSearchRepo(t, pg, "secret-ui-kit", "npm", "", false)
	newSearchRepo(t, pg, "public-ui-kit", "raw", "", true)
	newSearchRepo(t, pg, "webapp", "raw", "", false)
	newSearchRepo(t, pg, "empty-repo", "raw", "", false)

	// A package whose name contains the term, living in a repository whose own
	// name does not: it is a package hit, never a repository hit.
	blobID, err := pg.UpsertBlob(ctx, componentsBlobSHA, componentsBlobSHA, 5)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	var webappID string
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT id FROM repositories WHERE name = 'webapp'`).Scan(&webappID); err != nil {
		t.Fatalf("lookup webapp: %v", err)
	}

	execSQL(t, pg, `INSERT INTO raw_artifacts (repository_id, path, blob_id) VALUES ($1, 'acme/ui-kit-1.0.txt', $2)`, webappID, blobID)

	log := newTestLogger(t)
	h := search.NewHandler(pg.GetDB(), auth.NewEvaluator(pg, log, false), log)

	admin := &auth.Principal{Username: "admin", IsLegacy: true}
	alice := searchUser(t, pg, "alice", `ui-kit.*|webapp`)
	bob := searchUser(t, pg, "bob", "")
	anonymous := &auth.Principal{Username: "anonymous", IsAnonymous: true}

	run := func(t *testing.T, who *auth.Principal, rawQuery string) searchResponse {
		t.Helper()

		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/search?"+rawQuery, nil)
		r = r.WithContext(middleware.WithPrincipal(r.Context(), who))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("GET search?%s: want 200, got %d body=%s", rawQuery, w.Code, w.Body.String())
		}

		var resp searchResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}

		return resp
	}

	q := func(term string) string { return "q=" + url.QueryEscape(term) }

	tests := []struct {
		name     string
		who      *auth.Principal
		query    string
		wantRepo []string
		wantPkgs []string
	}{
		{
			name: "hyphenated substring finds every repository containing it", who: admin, query: q("ui-kit"),
			// Relevance order: the exact name, names starting with the term, then
			// names merely containing it (alphabetical within each).
			wantRepo: []string{"ui-kit", "ui-kit-docs", "my-ui-kit-lib", "public-ui-kit", "secret-ui-kit"},
			wantPkgs: []string{"webapp:acme/ui-kit-1.0.txt"},
		},
		{
			name: "prefix", who: admin, query: q("ui-kit-d"),
			wantRepo: []string{"ui-kit-docs"},
		},
		{
			name: "case-insensitive", who: admin, query: q("UI-KIT-DOCS"),
			wantRepo: []string{"ui-kit-docs"},
		},
		{
			name: "underscore is literal, not a one-character wildcard", who: admin, query: q("ui_kit"),
			wantRepo: []string{"ui_kit"},
		},
		{
			name: "repository without any artifacts is still found", who: admin, query: q("empty"),
			wantRepo: []string{"empty-repo"},
		},
		{
			name: "name qualifier", who: admin, query: q("name:docs"),
			wantRepo: []string{"ui-kit-docs"},
		},
		{
			name: "format qualifier", who: admin, query: q("ui-kit format:npm"),
			wantRepo: []string{"ui-kit", "secret-ui-kit"},
		},
		{
			name: "repo qualifier", who: admin, query: q("ui-kit repo:ui-kit"),
			wantRepo: []string{"ui-kit"},
		},
		{
			name: "sha256 identifies artifacts, never repositories", who: admin, query: q("sha256:" + componentsBlobSHA[:8]),
			wantPkgs: []string{"webapp:acme/ui-kit-1.0.txt"},
		},
		{
			name: "limit and offset page the repositories", who: admin, query: q("ui-kit") + "&limit=2&offset=1",
			wantRepo: []string{"ui-kit-docs", "my-ui-kit-lib"},
			wantPkgs: nil,
		},
		{
			name: "user only sees repositories their permissions allow", who: alice, query: q("ui-kit"),
			wantRepo: []string{"ui-kit", "ui-kit-docs"},
			wantPkgs: []string{"webapp:acme/ui-kit-1.0.txt"},
		},
		{
			name: "user without permissions sees nothing", who: bob, query: q("ui-kit"),
		},
		{
			name: "anonymous only sees public repositories", who: anonymous, query: q("ui-kit"),
			wantRepo: []string{"public-ui-kit"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := run(t, tc.who, tc.query)

			if got := resp.repoNames(); !slices.Equal(got, nonNil(tc.wantRepo)) {
				t.Errorf("repositories = %v, want %v", got, tc.wantRepo)
			}

			if got := resp.packageNames(); !slices.Equal(got, nonNil(tc.wantPkgs)) {
				t.Errorf("packages = %v, want %v", got, tc.wantPkgs)
			}
		})
	}

	t.Run("repository hit carries format and description", func(t *testing.T) {
		resp := run(t, admin, q("ui-kit")+"&limit=100")

		i := slices.IndexFunc(resp.Repositories, func(r search.RepositoryResult) bool { return r.Name == "ui-kit" })
		if i < 0 {
			t.Fatalf("ui-kit missing from %v", resp.repoNames())
		}

		if got := resp.Repositories[i]; got.Format != "npm" || got.Description != "shared components" {
			t.Errorf("ui-kit hit = %+v, want format npm and description %q", got, "shared components")
		}
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
