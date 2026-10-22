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
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// packageListDB records how the package listing was asked for.
type packageListDB struct {
	repoLifecycleDB

	plain    int
	searched []database.ComponentListOptions
}

func (db *packageListDB) ListComponentsByRepo(_ context.Context, _, _ string, _, _ int) ([]*models.Component, int, error) {
	db.plain++

	return nil, 0, nil
}

func (db *packageListDB) SearchComponents(_ context.Context, _ string, opts database.ComponentListOptions) ([]*models.Component, int, error) {
	db.searched = append(db.searched, opts)

	return nil, 0, nil
}

func TestPackagesListingQueryParams(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantStatus int
		wantPlain  bool
		want       database.ComponentListOptions
	}{
		{name: "default listing stays on the plain query", query: "page=2&limit=10", wantStatus: http.StatusOK, wantPlain: true},
		{name: "explicit created order is the default order", query: "sort=created", wantStatus: http.StatusOK, wantPlain: true},
		{
			name: "q is trimmed and passed through", query: "q=%20Wid%20", wantStatus: http.StatusOK,
			want: database.ComponentListOptions{Query: "Wid", Page: 1, Limit: 25},
		},
		{
			name: "sort=name with format, page and limit", query: "sort=name&format=npm&page=3&limit=50", wantStatus: http.StatusOK,
			want: database.ComponentListOptions{Format: "npm", SortByName: true, Page: 3, Limit: 50},
		},
		{
			name: "package_name keeps meaning an exact lookup", query: "package_name=%40acme%2Fwidget", wantStatus: http.StatusOK,
			want: database.ComponentListOptions{Name: "@acme/widget", Page: 1, Limit: 25},
		},
		{name: "unknown sort is rejected", query: "sort=size", wantStatus: http.StatusBadRequest},
		{name: "overlong q is rejected", query: "q=" + strings.Repeat("a", maxPackageQueryLen+1), wantStatus: http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &packageListDB{}
			db.targets = adminTargets()
			db.repo = &models.Repository{ID: "repo-1", Name: "pkgs", Format: "npm"}
			log := newFixTestLogger(t)
			h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

			router := chi.NewRouter()
			router.Get("/api/v1/repositories/{name}/packages", h.handlePackages)

			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, "/api/v1/repositories/pkgs/packages?"+tc.query, ""))

			if rw.Code != tc.wantStatus {
				t.Fatalf("status %d body %q, want %d", rw.Code, rw.Body.String(), tc.wantStatus)
			}

			if tc.wantStatus != http.StatusOK {
				if db.plain != 0 || len(db.searched) != 0 {
					t.Fatal("a rejected request must not reach the store")
				}

				return
			}

			if tc.wantPlain {
				if db.plain != 1 || len(db.searched) != 0 {
					t.Fatalf("plain=%d searched=%v, want one plain listing", db.plain, db.searched)
				}

				return
			}

			if db.plain != 0 || len(db.searched) != 1 || db.searched[0] != tc.want {
				t.Fatalf("plain=%d searched=%+v, want one search %+v", db.plain, db.searched, tc.want)
			}
		})
	}
}
