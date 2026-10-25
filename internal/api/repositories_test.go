// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

type repositoriesListMockDB struct {
	databasetest.BaseMockDB

	repos       []*models.Repository
	targets     []*models.PermissionTarget
	countCalled bool
}

func (db *repositoriesListMockDB) ListRepositories(_ context.Context, page, limit int, _ string) ([]*models.Repository, error) {
	start := (page - 1) * limit
	if start >= len(db.repos) {
		return nil, nil
	}

	end := min(start+limit, len(db.repos))

	return db.repos[start:end], nil
}

func (db *repositoriesListMockDB) CountRepositories(_ context.Context, _ string) (int, error) {
	db.countCalled = true
	return len(db.repos), nil
}

func (db *repositoriesListMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return db.targets, nil
}

func TestRepositoriesHandlerHandleListFiltersBeforePagination(t *testing.T) {
	db := &repositoriesListMockDB{
		repos: []*models.Repository{
			{Name: "hidden-before", Format: "rpm"},
			{Name: "visible-one", Format: "rpm"},
			{Name: "hidden-middle", Format: "rpm"},
			{Name: "visible-two", Format: "rpm"},
			{Name: "visible-three", Format: "rpm"},
			{Name: "hidden-after", Format: "rpm"},
		},
		targets: []*models.PermissionTarget{{
			RepositoryFilter: `visible-.*`,
			PathPattern:      `.*`,
			Actions:          []string{"read"},
		}},
	}
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer func() { _ = log.Sync() }()

	evaluator := auth.NewEvaluator(db, log, false)
	h := NewRepositoriesHandler(db, log, evaluator)
	userID := "user-1"
	principal := &auth.Principal{UserID: &userID, Username: "reader"}

	for _, tt := range []struct {
		page  int
		names []string
	}{
		{page: 1, names: []string{"visible-one", "visible-two"}},
		{page: 2, names: []string{"visible-three"}},
	} {
		t.Run("page-"+strconv.Itoa(tt.page), func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories?page="+strconv.Itoa(tt.page)+"&limit=2", nil)
			req = req.WithContext(middleware.WithPrincipal(req.Context(), principal))
			rw := httptest.NewRecorder()

			h.handleList(rw, req)

			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rw.Code, rw.Body.String())
			}

			var response struct {
				Total        int `json:"total"`
				Repositories []struct {
					Name string `json:"name"`
				} `json:"repositories"`
			}
			if err := json.NewDecoder(rw.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Total != 3 {
				t.Errorf("total = %d, want 3", response.Total)
			}

			if len(response.Repositories) != len(tt.names) {
				t.Fatalf("repositories = %d, want %d", len(response.Repositories), len(tt.names))
			}
			for i, repo := range response.Repositories {
				if repo.Name != tt.names[i] {
					t.Errorf("repository %d = %q, want %q", i, repo.Name, tt.names[i])
				}
			}
		})
	}

	if db.countCalled {
		t.Error("CountRepositories called for restricted principal")
	}
}
