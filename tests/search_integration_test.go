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
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/pkg/logger"
)

func TestSearchIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		skipOrFailMissingEnv(t, "TEST_DATABASE_URL")
	}

	cfg := database.DatabaseConfig{
		Driver:   "postgres",
		Host:     "localhost",
		Port:     5432,
		User:     "postgres",
		Password: "postgres",
		Database: "artifact_repo",
		SSLMode:  "disable",
	}

	pgdb, err := database.NewPostgresDB(cfg, nil)
	if err != nil {
		t.Skipf("cannot connect to database: %v", err)
	}
	defer pgdb.Close()

	ctx := context.Background()
	if err := pgdb.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sqldb := pgdb.GetDB()

	var repoID string
	err = sqldb.QueryRowContext(ctx,
		`INSERT INTO repositories (name, format) VALUES ('search-test-repo', 'rpm')
		 ON CONFLICT (name) DO UPDATE SET format='rpm' RETURNING id`,
	).Scan(&repoID)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	t.Cleanup(func() {
		sqldb.ExecContext(context.Background(), `DELETE FROM components WHERE repository_id=$1`, repoID)
		sqldb.ExecContext(context.Background(), `DELETE FROM repositories WHERE id=$1`, repoID)
	})

	_, err = sqldb.ExecContext(ctx,
		`INSERT INTO components (repository_id, format, name, version, description, summary, license)
		 VALUES ($1, 'rpm', 'search-test-package', '1.0.0', 'A searchable test package', 'Test summary', 'MIT')
		 ON CONFLICT DO NOTHING`,
		repoID,
	)
	if err != nil {
		t.Fatalf("insert component: %v", err)
	}

	log, _ := logger.New("error", "json", "stderr")
	ev := auth.NewEvaluator(pgdb, log, true)
	h := search.NewHandler(sqldb, ev, log)

	adminID := "integration-test-admin"
	principal := &auth.Principal{UserID: &adminID, Username: "admin", IsLegacy: true}

	r := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/search?q=search-test-package", nil)
	r = r.WithContext(middleware.WithPrincipal(r.Context(), principal))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Total   int64                 `json:"total"`
		Results []search.SearchResult `json:"results"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total == 0 {
		t.Error("expected at least 1 result")
	}

	rMissing := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/search", nil)
	rMissing = rMissing.WithContext(middleware.WithPrincipal(rMissing.Context(), principal))
	wMissing := httptest.NewRecorder()
	h.ServeHTTP(wMissing, rMissing)
	if wMissing.Code != http.StatusBadRequest {
		t.Errorf("missing q: want 400, got %d", wMissing.Code)
	}
}
