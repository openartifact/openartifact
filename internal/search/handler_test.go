// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

type mockSearcher struct {
	count   int64
	results []SearchResult
	repos   []RepositoryResult
	err     error
	repoErr error
	gotOpts PageOptions
}

func (m *mockSearcher) SearchRepositories(_ context.Context, _ SearchFilter, _, _ int, _ map[uuid.UUID]struct{}) ([]RepositoryResult, error) {
	return m.repos, m.repoErr
}

func (m *mockSearcher) SearchPage(_ context.Context, _ SearchFilter, opts PageOptions, _, _ int, _ map[uuid.UUID]struct{}) ([]SearchResult, int64, error) {
	m.gotOpts = opts

	if m.err != nil {
		return nil, 0, m.err
	}

	return m.results, m.count, nil
}

func newTestHandler(t *testing.T, ms *mockSearcher) (*Handler, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	mock.ExpectQuery(`SELECT id, name, project_id FROM repositories`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "project_id"}))

	log, _ := logger.New("error", "json", "stderr")
	ev := auth.NewEvaluator(nil, log, true)
	h := &Handler{
		searcher:  ms,
		db:        db,
		evaluator: ev,
		log:       log,
	}

	return h, func() { _ = db.Close() }
}

func adminPrincipal() *auth.Principal {
	id := "test-user-id"
	return &auth.Principal{UserID: &id, Username: "admin", IsLegacy: true}
}

func requestWithPrincipal(method, target string, p *auth.Principal) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	if p != nil {
		r = r.WithContext(middleware.WithPrincipal(r.Context(), p))
	}

	return r
}

func TestHandlerMissingQ(t *testing.T) {
	h, cleanup := newTestHandler(t, &mockSearcher{})
	defer cleanup()

	w := httptest.NewRecorder()
	r := requestWithPrincipal("GET", "/api/v1/search", adminPrincipal())
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandlerLimitTooHigh(t *testing.T) {
	h, cleanup := newTestHandler(t, &mockSearcher{count: 1})
	defer cleanup()

	w := httptest.NewRecorder()
	r := requestWithPrincipal("GET", "/api/v1/search?q=test&limit=200", adminPrincipal())
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandlerCountTooHigh(t *testing.T) {
	h, cleanup := newTestHandler(t, &mockSearcher{count: 10001})
	defer cleanup()

	w := httptest.NewRecorder()
	r := requestWithPrincipal("GET", "/api/v1/search?q=test", adminPrincipal())
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandlerSuccessfulSearch(t *testing.T) {
	ms := &mockSearcher{
		count: 1,
		results: []SearchResult{
			{Repository: "my-repo", Format: "npm", Name: "lodash", Version: "4.17.21", CreatedAt: time.Now()},
		},
	}

	h, cleanup := newTestHandler(t, ms)
	defer cleanup()

	db, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	h.db = db

	repoID := uuid.New().String()
	mock2.ExpectQuery(`SELECT id, name, project_id FROM repositories`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "project_id"}).AddRow(repoID, "my-repo", nil))

	w := httptest.NewRecorder()
	r := requestWithPrincipal("GET", "/api/v1/search?q=lodash", adminPrincipal())
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["total"] == nil {
		t.Error("missing 'total' in response")
	}

	if resp["packages"] == nil {
		t.Error("missing 'packages' in response")
	}

	if _, dup := resp["results"]; dup {
		t.Error("response still carries the duplicate 'results' key")
	}
}

func TestHandlerEmptyResult(t *testing.T) {
	ms := &mockSearcher{count: 0, results: nil}

	h, cleanup := newTestHandler(t, ms)
	defer cleanup()

	db, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	h.db = db

	mock2.ExpectQuery(`SELECT id, name, project_id FROM repositories`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "project_id"}))

	w := httptest.NewRecorder()
	r := requestWithPrincipal("GET", "/api/v1/search?q=nothing", adminPrincipal())
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp map[string]any

	_ = json.NewDecoder(w.Body).Decode(&resp)

	results, ok := resp["packages"].([]any)
	if !ok {
		t.Fatalf("packages not found or not an array")
	}

	if len(results) != 0 {
		t.Errorf("want empty results, got %v", results)
	}
}

// Repository hits are real repositories (name/format/description), not
// version-less package rows: a raw artifact or docker tag stays under
// "packages" and must never be offered as a repository to navigate to.
func TestHandlerSeparatesRepositoriesFromPackages(t *testing.T) {
	ms := &mockSearcher{
		count: 1,
		results: []SearchResult{
			{Repository: "webapp", Format: "raw", Name: "acme/ui-kit-1.0.txt", CreatedAt: time.Now()},
		},
		repos: []RepositoryResult{
			{Name: "ui-kit", Format: "npm", Description: "shared components"},
		},
	}

	h, cleanup := newTestHandler(t, ms)
	defer cleanup()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, requestWithPrincipal("GET", "/api/v1/search?q=ui-kit", adminPrincipal()))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Packages     []SearchResult     `json:"packages"`
		Repositories []RepositoryResult `json:"repositories"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Packages) != 1 || resp.Packages[0].Name != "acme/ui-kit-1.0.txt" {
		t.Errorf("version-less package row must stay in packages, got %+v", resp.Packages)
	}

	if len(resp.Repositories) != 1 || resp.Repositories[0] != (RepositoryResult{Name: "ui-kit", Format: "npm", Description: "shared components"}) {
		t.Errorf("want the ui-kit repository hit, got %+v", resp.Repositories)
	}
}

func TestHandlerRepositorySearchErrorIs500(t *testing.T) {
	h, cleanup := newTestHandler(t, &mockSearcher{repoErr: context.DeadlineExceeded})
	defer cleanup()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, requestWithPrincipal("GET", "/api/v1/search?q=ui-kit", adminPrincipal()))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", w.Code)
	}
}

func TestHandlerRejectsUnboundedWork(t *testing.T) {
	for name, target := range map[string]string{
		"offset": "/search?q=a&offset=10001",
		"repos":  "/search?q=a" + strings.Repeat("&repo=x", maxFilterValues+1),
		"format": "/search?q=a" + strings.Repeat("&format=x", maxFilterValues+1),
	} {
		t.Run(name, func(t *testing.T) {
			h, cleanup := newTestHandler(t, &mockSearcher{})
			defer cleanup()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, requestWithPrincipal(http.MethodGet, target, adminPrincipal()))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}
