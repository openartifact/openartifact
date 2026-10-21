// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type formatCapturingDB struct {
	database.BaseMockDB

	repo   *models.Repository
	filter string
}

func (d *formatCapturingDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return d.repo, nil
}

func (d *formatCapturingDB) ListComponentsByRepo(_ context.Context, _, formatFilter string, _, _ int) ([]*models.Component, int, error) {
	d.filter = formatFilter

	return nil, 0, nil
}

// The package listing narrows to the repository's own format unless the
// caller chose one, so the query touches a single format table.
func TestHandlePackages_DefaultsFormatFilterToRepositoryFormat(t *testing.T) {
	cases := []struct {
		name       string
		repoFormat string
		query      string
		want       string
	}{
		{"defaults to repo format", "maven", "", "maven"},
		{"explicit format wins", "maven", "?format=npm", "npm"},
		{"unknown repo format stays unfiltered", "weird", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, err := logger.New("error", "json", "/dev/null")
			if err != nil {
				t.Fatal(err)
			}

			db := &formatCapturingDB{repo: &models.Repository{ID: "r1", Name: "myrepo", Format: tc.repoFormat}}
			h := NewRepositoriesHandler(db, log, nil)

			router := chi.NewRouter()
			router.Get("/api/v1/repositories/{name}/packages", h.handlePackages)

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet,
				"/api/v1/repositories/myrepo/packages"+tc.query, http.NoBody))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}

			if db.filter != tc.want {
				t.Errorf("format filter = %q, want %q", db.filter, tc.want)
			}
		})
	}
}
