// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
)

// TestSearchRequestLatency measures whole search requests, as the command
// palette and the search page make them, against 1000 repositories and 30000
// helm chart versions (3000 charts x 10 versions) for a user whose
// permissions cover every repository. It is a measurement, not a check, and
// only runs with OA_SEARCH_BENCH=1 (testing.Benchmark needs a *testing.T here
// because the database helpers do):
//
//	OA_SEARCH_BENCH=1 TEST_DATABASE_URL=... go test -tags integration -run SearchRequestLatency -v ./tests/
func TestSearchRequestLatency(t *testing.T) {
	if os.Getenv("OA_SEARCH_BENCH") == "" {
		t.Skip("set OA_SEARCH_BENCH=1 to measure")
	}

	pg := freshPostgresCfg(t, 10, func(cfg *database.DatabaseConfig) { cfg.AuthCacheTTLMillis = 2000 })
	seedSearchBench(t, pg)

	log := newTestLogger(t)
	h := search.NewHandler(pg.GetDB(), auth.NewEvaluator(pg, log, false), log, search.WithAuthCache(pg))
	alice := searchUser(t, pg, "alice", ".*")

	for _, req := range []struct{ name, target string }{
		{"palette_selective", "/api/v1/search?q=chart-1234&limit=8&group=package"},
		{"palette_broad", "/api/v1/search?q=chart-12&limit=8&group=package"},
		{"page_broad", "/api/v1/search?q=chart-12&limit=25"},
		{"palette_widest", "/api/v1/search?q=chart-&limit=8&group=package"},
		{"repo_name", "/api/v1/search?q=repo-77&limit=8"},
	} {
		res := testing.Benchmark(func(b *testing.B) {
			b.Helper()

			for b.Loop() {
				r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, req.target, nil)
				r = r.WithContext(middleware.WithPrincipal(r.Context(), alice))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)

				// "chart-" matches 30000 rows: refused as too broad (400) after
				// the capped count, which is the cost being measured.
				if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
					b.Fatalf("%s: status %d: %s", req.target, w.Code, w.Body.String())
				}
			}
		})

		t.Logf("%-18s %s", req.name, res)
	}
}

func seedSearchBench(t *testing.T, pg *database.PostgresDB) {
	t.Helper()

	for i := range 1000 {
		newSearchRepo(t, pg, fmt.Sprintf("repo-%d", i), "raw", "", false)
	}

	newSearchRepo(t, pg, "charts", "helm", "", false)

	blobID, err := pg.UpsertBlob(context.Background(), componentsBlobSHA, componentsBlobSHA, 5)
	if err != nil {
		t.Fatalf("UpsertBlob: %v", err)
	}

	execSQL(t, pg, `INSERT INTO helm_charts (repository_id, name, version, blob_id, created_at)
		SELECT (SELECT id FROM repositories WHERE name = 'charts'), 'chart-' || c, v || '.0.0', $1,
		       now() - (c * v) * interval '1 minute'
		FROM generate_series(1, 3000) c, generate_series(1, 10) v`, blobID)
	// Hint bits and statistics settled, as in a database that has been running.
	execSQL(t, pg, `VACUUM ANALYZE`)
}
