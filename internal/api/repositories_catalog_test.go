// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// catalogDB serves canned catalog aggregates over the repository list mock.
type catalogDB struct {
	repositoriesListMockDB

	stats       map[string]models.RepoStats
	counts      map[string]int64
	statsCalls  int
	rollup      []models.PackageRollup
	rollupOpts  models.PackageRollupOptions
	security    map[string]models.ArtifactSecurity
	recent      []models.RecentPackage
	recentRepos []string
	recentLimit int
	repo        *models.Repository
}

func (db *catalogDB) RepoCatalogStats(_ context.Context, ids []string, _ bool) (map[string]models.RepoStats, error) {
	db.statsCalls++

	out := map[string]models.RepoStats{}

	for _, id := range ids {
		out[id] = db.stats[id]
	}

	return out, nil
}

func (db *catalogDB) CountRepoPackageNames(context.Context, []string) (map[string]int64, error) {
	return db.counts, nil
}

func (db *catalogDB) ListRepoPackageRollup(_ context.Context, opts models.PackageRollupOptions) ([]models.PackageRollup, int, error) {
	db.rollupOpts = opts
	return db.rollup, len(db.rollup), nil
}

func (db *catalogDB) ArtifactSecurity(context.Context, string, []string) (map[string]models.ArtifactSecurity, error) {
	return db.security, nil
}

func (db *catalogDB) ListRecentPackages(_ context.Context, ids []string, limit int) ([]models.RecentPackage, error) {
	db.recentRepos, db.recentLimit = ids, limit
	return db.recent, nil
}

func (db *catalogDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if db.repo != nil && db.repo.Name == name {
		return db.repo, nil
	}

	return nil, nil
}

func newCatalogHandler(t *testing.T, db *catalogDB, targets []*models.PermissionTarget) *RepositoriesHandler {
	t.Helper()

	db.targets = targets
	log := newFixTestLogger(t)

	return NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))
}

func serveCatalog(h *RepositoriesHandler, target string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Get("/api/v1/repositories", h.handleList)
	router.Get("/api/v1/packages/recent", h.handleRecentPackages)
	router.Get("/api/v1/repositories/{name}/packages", h.handlePackages)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, target, ""))

	return rw
}

func listedNames(t *testing.T, rw *httptest.ResponseRecorder) []string {
	t.Helper()

	if rw.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rw.Code, rw.Body.String())
	}

	var body struct {
		Repositories []struct {
			Name string `json:"name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	names := make([]string, 0, len(body.Repositories))
	for _, r := range body.Repositories {
		names = append(names, r.Name)
	}

	return names
}

func catalogRepos() (*catalogDB, []string) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	older := now.Add(-48 * time.Hour)
	scanned := now.Add(-time.Hour)

	db := &catalogDB{
		repos: []*models.Repository{
			{ID: "r1", Name: "alpha", Format: "npm", RepoType: "hosted"},
			{ID: "r2", Name: "beta", Format: "npm", RepoType: "hosted"},
			{ID: "r3", Name: "gamma-npm", Format: "npm", RepoType: "hosted"},
		},
		stats: map[string]models.RepoStats{
			"r1": {
				SizeBytes: 100, LastPublishedAt: &older,
				Security: models.RepoSecurity{ScannedAt: &scanned},
				Quota:    models.RepoQuotaUsage{UsedBytes: 100},
			},
			"r2": {
				SizeBytes: 900, LastPublishedAt: &now,
				Security: models.RepoSecurity{SecurityCounts: models.SecurityCounts{Critical: 2}, ScannedAt: &scanned},
				Quota:    models.RepoQuotaUsage{LimitBytes: 1000, UsedBytes: 900},
			},
			"r3": {SizeBytes: 500, Quota: models.RepoQuotaUsage{UsedBytes: 500}},
		},
		counts: map[string]int64{"r1": 3, "r2": 7, "r3": 0},
	}

	return db, []string{"alpha", "beta", "gamma-npm"}
}

func TestRepositoriesListStatsAndFilters(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
		code  int
	}{
		{name: "default keeps list order", query: "", want: []string{"alpha", "beta", "gamma-npm"}},
		{name: "name substring", query: "q=ETA", want: []string{"beta"}},
		{name: "flagged", query: "flagged=true", want: []string{"beta"}},
		{name: "near quota", query: "near_quota=true", want: []string{"beta"}},
		{name: "near quota with a higher threshold", query: "near_quota=true&quota_threshold=0.95", want: nil},
		{name: "unscanned", query: "unscanned=true", want: []string{"gamma-npm"}},
		{name: "sort by size", query: "sort=size", want: []string{"beta", "gamma-npm", "alpha"}},
		{name: "sort by last published", query: "sort=last_published", want: []string{"beta", "alpha", "gamma-npm"}},
		{name: "sort by name", query: "sort=name", want: []string{"alpha", "beta", "gamma-npm"}},
		{name: "bad sort", query: "sort=weird", code: http.StatusBadRequest},
		{name: "bad flag", query: "flagged=maybe", code: http.StatusBadRequest},
		{name: "bad threshold", query: "quota_threshold=2", code: http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := catalogRepos()
			h := newCatalogHandler(t, db, adminTargets())

			rw := serveCatalog(h, "/api/v1/repositories?"+tc.query)
			if tc.code != 0 {
				if rw.Code != tc.code {
					t.Fatalf("status %d, want %d", rw.Code, tc.code)
				}

				return
			}

			if got := listedNames(t, rw); !slices.Equal(got, tc.want) && (len(got) != 0 || len(tc.want) != 0) {
				t.Fatalf("names %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRepositoriesListStatsShapeAndNoNPlusOne(t *testing.T) {
	db, _ := catalogRepos()
	h := newCatalogHandler(t, db, adminTargets())

	rw := serveCatalog(h, "/api/v1/repositories?page=1&limit=10")

	var body struct {
		Total        int `json:"total"`
		Repositories []struct {
			Name  string `json:"name"`
			Stats struct {
				PackageCount    int64   `json:"package_count"`
				SizeBytes       int64   `json:"size_bytes"`
				LastPublishedAt *string `json:"last_published_at"`
				Security        struct {
					Critical  int64   `json:"critical"`
					ScannedAt *string `json:"scanned_at"`
				} `json:"security"`
				Quota struct {
					LimitBytes *int64 `json:"limit_bytes"`
					UsedBytes  int64  `json:"used_bytes"`
				} `json:"quota"`
			} `json:"stats"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Total != 3 || len(body.Repositories) != 3 {
		t.Fatalf("total %d items %d", body.Total, len(body.Repositories))
	}

	beta := body.Repositories[1]
	if beta.Name != "beta" || beta.Stats.PackageCount != 7 || beta.Stats.SizeBytes != 900 || beta.Stats.Security.Critical != 2 ||
		beta.Stats.Security.ScannedAt == nil || beta.Stats.LastPublishedAt == nil ||
		beta.Stats.Quota.LimitBytes == nil || *beta.Stats.Quota.LimitBytes != 1000 || beta.Stats.Quota.UsedBytes != 900 {
		t.Fatalf("beta stats %+v", beta.Stats)
	}

	if gamma := body.Repositories[2]; gamma.Stats.Quota.LimitBytes != nil || gamma.Stats.LastPublishedAt != nil || gamma.Stats.Security.ScannedAt != nil {
		t.Fatalf("unlimited, never published, unscanned repository must omit those fields: %+v", gamma.Stats)
	}

	if db.statsCalls != 1 {
		t.Fatalf("stats queried %d times for a page, want one grouped query", db.statsCalls)
	}
}

func TestRepositoriesListStatsRespectReadPermission(t *testing.T) {
	db, _ := catalogRepos()
	h := newCatalogHandler(t, db, []*models.PermissionTarget{{RepositoryFilter: `beta`, PathPattern: `.*`, Actions: []string{"read"}}})

	if got := listedNames(t, serveCatalog(h, "/api/v1/repositories?sort=size")); !slices.Equal(got, []string{"beta"}) {
		t.Fatalf("names %v", got)
	}
}

func TestPackagesRollup(t *testing.T) {
	scanned := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	db := &catalogDB{
		repo: &models.Repository{ID: "r1", Name: "pkgs", Format: "npm"},
		rollup: []models.PackageRollup{
			{Format: "npm", Name: "@acme/widget", LatestVersion: "2.0.0", VersionCount: 4, SizeBytes: 55, UpdatedAt: scanned, LatestIDs: []string{"a", "b"}},
			{Format: "npm", Name: "plain", LatestVersion: "1.0.0", VersionCount: 1, LatestIDs: []string{"c"}},
		},
		security: map[string]models.ArtifactSecurity{
			"a": {SecurityCounts: models.SecurityCounts{Critical: 1, Low: 2}, ScannedAt: &scanned},
			"b": {SecurityCounts: models.SecurityCounts{High: 3}},
		},
	}
	h := newCatalogHandler(t, db, adminTargets())

	rw := serveCatalog(h, "/api/v1/repositories/pkgs/packages?group=package&flagged=true&sort=name&q=wid")
	if rw.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rw.Code, rw.Body.String())
	}

	if o := db.rollupOpts; o.RepoID != "r1" || !o.Flagged || !o.SortByName || o.Query != "wid" {
		t.Fatalf("rollup options %+v", o)
	}

	var body struct {
		Total    int `json:"total"`
		Packages []struct {
			Name          string `json:"name"`
			LatestVersion string `json:"latest_version"`
			VersionCount  int    `json:"version_count"`
			SizeBytes     int64  `json:"size_bytes"`
			Security      struct {
				Critical, High, Low int64
			} `json:"security"`
			ScannedAt *string `json:"scanned_at"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	first, second := body.Packages[0], body.Packages[1]
	if body.Total != 2 || first.LatestVersion != "2.0.0" || first.VersionCount != 4 || first.SizeBytes != 55 ||
		first.Security.Critical != 1 || first.Security.High != 3 || first.Security.Low != 2 || first.ScannedAt == nil {
		t.Fatalf("rollup row %+v (total %d)", first, body.Total)
	}

	if second.ScannedAt != nil || second.Security.Critical != 0 {
		t.Fatalf("unscanned package must have no scanned_at and zero counts: %+v", second)
	}
}

func TestPackagesRollupRejectsBadParams(t *testing.T) {
	db := &catalogDB{repo: &models.Repository{ID: "r1", Name: "pkgs", Format: "npm"}}
	h := newCatalogHandler(t, db, adminTargets())

	for _, q := range []string{"group=version", "flagged=true", "group=package&flagged=perhaps"} {
		if rw := serveCatalog(h, "/api/v1/repositories/pkgs/packages?"+q); rw.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, rw.Code)
		}
	}
}

func TestPackagesRollupReadPermission(t *testing.T) {
	db := &catalogDB{repo: &models.Repository{ID: "r1", Name: "pkgs", Format: "npm"}}
	h := newCatalogHandler(t, db, []*models.PermissionTarget{{RepositoryFilter: `other`, PathPattern: `.*`, Actions: []string{"read"}}})

	if rw := serveCatalog(h, "/api/v1/repositories/pkgs/packages?group=package"); rw.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rw.Code)
	}
}

func TestRecentPackages(t *testing.T) {
	created := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)

	newDB := func() *catalogDB {
		return &catalogDB{
			repos: []*models.Repository{
				{ID: "r1", Name: "readable", Format: "npm"},
				{ID: "r2", Name: "writable", Format: "npm"},
				{ID: "r3", Name: "secret", Format: "npm"},
			},
			recent: []models.RecentPackage{{Repository: "writable", Format: "npm", Name: "left-pad", Version: "1.0.0", CreatedAt: created}},
		}
	}
	targets := []*models.PermissionTarget{
		{RepositoryFilter: `readable`, PathPattern: `.*`, Actions: []string{"read"}},
		{RepositoryFilter: `writable`, PathPattern: `.*`, Actions: []string{"read", "write"}},
	}

	t.Run("readable repositories only", func(t *testing.T) {
		db := newDB()
		h := newCatalogHandler(t, db, targets)

		rw := serveCatalog(h, "/api/v1/packages/recent?limit=5")
		if rw.Code != http.StatusOK {
			t.Fatalf("status %d body %s", rw.Code, rw.Body.String())
		}

		if !slices.Equal(db.recentRepos, []string{"r1", "r2"}) || db.recentLimit != 5 {
			t.Fatalf("queried repos %v limit %d", db.recentRepos, db.recentLimit)
		}

		var body struct {
			Packages []struct {
				Repository string `json:"repository"`
				Name       string `json:"name"`
				Version    string `json:"version"`
				CreatedAt  string `json:"created_at"`
			} `json:"packages"`
		}
		if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil || len(body.Packages) != 1 ||
			body.Packages[0].Repository != "writable" || body.Packages[0].CreatedAt == "" {
			t.Fatalf("body %s err %v", rw.Body.String(), err)
		}
	})

	t.Run("mine narrows to writable repositories", func(t *testing.T) {
		db := newDB()
		h := newCatalogHandler(t, db, targets)

		if rw := serveCatalog(h, "/api/v1/packages/recent?mine=true"); rw.Code != http.StatusOK {
			t.Fatalf("status %d", rw.Code)
		}

		if !slices.Equal(db.recentRepos, []string{"r2"}) || db.recentLimit != defaultRecentLimit {
			t.Fatalf("queried repos %v limit %d", db.recentRepos, db.recentLimit)
		}
	})

	t.Run("limit is capped and bad mine rejected", func(t *testing.T) {
		db := newDB()
		h := newCatalogHandler(t, db, targets)

		serveCatalog(h, "/api/v1/packages/recent?limit=9999")

		if db.recentLimit != maxRecentLimit {
			t.Fatalf("limit %d, want %d", db.recentLimit, maxRecentLimit)
		}

		if rw := serveCatalog(h, "/api/v1/packages/recent?mine=x"); rw.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", rw.Code)
		}
	})
}

// detailDB serves one package version and the listing rows of its files.
type detailDB struct {
	catalogDB

	detail *models.ComponentDetail
	rows   []*models.Component
}

func (db *detailDB) GetRepoComponentDetail(_ context.Context, repoID, id string) (*models.ComponentDetail, error) {
	if repoID == "r1" && db.detail != nil && db.detail.ID == id {
		return db.detail, nil
	}

	return nil, nil
}

func (db *detailDB) SearchComponents(context.Context, string, database.ComponentListOptions) ([]*models.Component, int, error) {
	return db.rows, len(db.rows), nil
}

func TestPackageDetail(t *testing.T) {
	db := &detailDB{
		repo: &models.Repository{ID: "r1", Name: "pkgs", Format: "npm"},
		rollup: []models.PackageRollup{
			{Format: "npm", Name: "widget", LatestVersion: "2.0.0", VersionCount: 3},
		},
		detail: &models.ComponentDetail{
			ID: "p1", Format: "npm", Name: "widget", Version: "1.0.0", Filename: "widget-1.0.0.tgz",
			Info: map[string]string{"main": "index.js", "author": "me"},
			Deps: []byte(`{"dependencies":{"left-pad":"^1.0.0"}}`),
		},
		rows: []*models.Component{
			{ID: "p1", Name: "widget", Version: "1.0.0", Format: "npm", Filename: "widget-1.0.0.tgz"},
			{ID: "p2", Name: "widget", Version: "1.0.0-rc.1", Format: "npm", Filename: "widget-1.0.0-rc.1.tgz"},
		},
	}
	h := newCatalogHandler(t, &db.catalogDB, adminTargets())
	h.db = db

	router := chi.NewRouter()
	router.Get("/api/v1/repositories/{name}/packages/{id}", h.handlePackageDetail)

	get := func(target string) *httptest.ResponseRecorder {
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, target, ""))

		return rw
	}

	rw := get("/api/v1/repositories/pkgs/packages/p1")
	if rw.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rw.Code, rw.Body.String())
	}

	var body struct {
		Name          string `json:"name"`
		VersionCount  int    `json:"version_count"`
		LatestVersion string `json:"latest_version"`
		Metadata      []struct{ Key, Value string }
		Files         []struct{ ID string }
		Dependencies  []struct{ Name string }
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Name != "widget" || body.VersionCount != 3 || body.LatestVersion != "2.0.0" {
		t.Fatalf("body %+v", body)
	}

	if len(body.Metadata) != 2 || body.Metadata[0].Key != "author" {
		t.Fatalf("metadata must be sorted by key: %+v", body.Metadata)
	}

	if len(body.Files) != 1 || body.Files[0].ID != "p1" {
		t.Fatalf("only the exact version's files are listed: %+v", body.Files)
	}

	if len(body.Dependencies) != 1 || body.Dependencies[0].Name != "left-pad" {
		t.Fatalf("dependencies %+v", body.Dependencies)
	}

	if rw := get("/api/v1/repositories/pkgs/packages/unknown"); rw.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status %d, want 404", rw.Code)
	}
}

func TestPackageDetailReadPermission(t *testing.T) {
	db := &detailDB{repo: &models.Repository{ID: "r1", Name: "pkgs", Format: "npm"}}
	h := newCatalogHandler(t, &db.catalogDB, []*models.PermissionTarget{{RepositoryFilter: `other`, PathPattern: `.*`, Actions: []string{"read"}}})
	h.db = db

	router := chi.NewRouter()
	router.Get("/api/v1/repositories/{name}/packages/{id}", h.handlePackageDetail)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, "/api/v1/repositories/pkgs/packages/p1", ""))

	if rw.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rw.Code)
	}
}
