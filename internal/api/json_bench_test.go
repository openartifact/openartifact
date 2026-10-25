// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type benchResponseWriter struct {
	header http.Header
	n      int
}

func (w *benchResponseWriter) Header() http.Header { return w.header }
func (w *benchResponseWriter) WriteHeader(int)     {}
func (w *benchResponseWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

func benchComponents(n int) []*models.Component {
	comps := make([]*models.Component, n)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for i := range comps {
		comps[i] = &models.Component{
			ID:           "comp-" + strconv.Itoa(i),
			RepositoryID: "repo-1",
			SHA256:       fmt.Sprintf("%064x", i),
			Format:       "rpm",
			Name:         "libexample" + strconv.Itoa(i),
			Version:      "1.2." + strconv.Itoa(i%50),
			Arch:         "x86_64",
			PackageSize:  123456,
			Filename:     "libexample" + strconv.Itoa(i) + "-1.2.0-1.x86_64.rpm",
			CreatedAt:    created.Add(time.Duration(i) * time.Minute),
		}
	}

	return comps
}

// packageJSON mirrors the keys of the map built by handlePackages.
type packageJSON struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Format    string    `json:"format"`
	Arch      string    `json:"arch"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Filename  string    `json:"filename"`
	CreatedAt time.Time `json:"created_at"`
}

type packagesPageJSON struct {
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	Limit    int           `json:"limit"`
	Packages []packageJSON `json:"packages"`
}

// BenchmarkPackagesJSON compares the response shape handlePackages builds
// ([]map[string]any inside map[string]any) with an equivalent typed struct,
// including the build step. n above 100 is only reachable through other
// endpoints that return whole lists; handlePackages itself clamps limit to 100.
func BenchmarkPackagesJSON(b *testing.B) {
	for _, n := range []int{100, 10000} {
		components := benchComponents(n)

		b.Run(fmt.Sprintf("map/entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &benchResponseWriter{header: http.Header{}}

			for b.Loop() {
				packages := make([]map[string]any, 0, len(components))
				for _, c := range components {
					packages = append(packages, map[string]any{
						"id":         c.ID,
						"name":       c.Name,
						"version":    c.Version,
						"format":     c.Format,
						"arch":       c.Arch,
						"size":       c.PackageSize,
						"sha256":     c.SHA256,
						"filename":   c.Filename,
						"created_at": c.CreatedAt,
					})
				}

				writeJSON(w, http.StatusOK, map[string]any{"total": n, "page": 1, "limit": n, "packages": packages})
			}

			b.SetBytes(int64(w.n / b.N))
		})

		b.Run(fmt.Sprintf("typed/entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &benchResponseWriter{header: http.Header{}}

			for b.Loop() {
				packages := make([]packageJSON, 0, len(components))
				for _, c := range components {
					packages = append(packages, packageJSON{
						ID: c.ID, Name: c.Name, Version: c.Version, Format: c.Format, Arch: c.Arch,
						Size: c.PackageSize, SHA256: c.SHA256, Filename: c.Filename, CreatedAt: c.CreatedAt,
					})
				}

				writeJSON(w, http.StatusOK, packagesPageJSON{Total: n, Page: 1, Limit: n, Packages: packages})
			}

			b.SetBytes(int64(w.n / b.N))
		})
	}
}

type benchPackagesDB struct {
	databasetest.BaseMockDB

	repo       *models.Repository
	components []*models.Component
}

func (d *benchPackagesDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return d.repo, nil
}

func (d *benchPackagesDB) ListComponentsByRepo(_ context.Context, _, _ string, _, limit int) ([]*models.Component, int, error) {
	return d.components[:min(limit, len(d.components))], len(d.components), nil
}

// BenchmarkHandlePackages runs the real handler (anonymous principal, so no
// permission check) through chi at its maximum page size.
func BenchmarkHandlePackages(b *testing.B) {
	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		b.Fatal(err)
	}

	db := &benchPackagesDB{repo: &models.Repository{ID: "repo-1", Name: "myrepo"}, components: benchComponents(1000)}
	h := NewRepositoriesHandler(db, log, nil)

	router := chi.NewRouter()
	router.Get("/api/v1/repositories/{name}/packages", h.handlePackages)

	for _, limit := range []int{25, 100} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
			fmt.Sprintf("/api/v1/repositories/myrepo/packages?limit=%d", limit), http.NoBody)

		b.Run(fmt.Sprintf("limit=%d", limit), func(b *testing.B) {
			b.ReportAllocs()

			w := &benchResponseWriter{header: http.Header{}}

			for b.Loop() {
				router.ServeHTTP(w, req)
			}

			if w.n == 0 {
				b.Fatal("no body written")
			}
		})
	}
}

// BenchmarkRepositoryListJSON measures RepositoryToResponse plus encoding of
// the already-typed repository list, the reference for the map-based shapes.
func BenchmarkRepositoryListJSON(b *testing.B) {
	for _, n := range []int{100, 10000} {
		repos := make([]*models.Repository, n)
		for i := range repos {
			repos[i] = &models.Repository{
				ID: "repo-" + strconv.Itoa(i), Name: "repo" + strconv.Itoa(i), Format: "npm",
				Description: "A repository", Public: i%2 == 0, RepoType: "hosted",
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}
		}

		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &benchResponseWriter{header: http.Header{}}

			for b.Loop() {
				list := make([]responses.RepositoryResponse, 0, len(repos))
				for _, r := range repos {
					list = append(list, RepositoryToResponse(r))
				}

				writeJSON(w, http.StatusOK, map[string]any{"total": n, "page": 1, "limit": n, "repositories": list})
			}

			b.SetBytes(int64(w.n / b.N))
		})
	}
}

func benchAuditLogs(n int) []*models.AuditLog {
	logs := make([]*models.AuditLog, n)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	actor, repoID, rtype, rid, ip := "actor-1", "repo-1", "package", "pkg-1", "203.0.113.5"

	for i := range logs {
		logs[i] = &models.AuditLog{
			ID:           "audit-" + strconv.Itoa(i),
			Action:       "package.upload",
			ActorID:      &actor,
			RepositoryID: &repoID,
			ResourceType: &rtype,
			ResourceID:   &rid,
			IPAddress:    &ip,
			CreatedAt:    created.Add(time.Duration(i) * time.Second),
			DetailsJSON: []byte(`{"actor":"jane","name":"lodash","version":"4.17.21",` +
				`"message":"{\"event\":\"upload\",\"size\":12345}"}`),
		}
	}

	return logs
}

type auditEntryJSON struct {
	ID           string  `json:"id"`
	Timestamp    any     `json:"timestamp"`
	Action       string  `json:"action"`
	Actor        string  `json:"actor"`
	Target       *string `json:"target"`
	Details      any     `json:"details"`
	IP           *string `json:"ip"`
	RepositoryID *string `json:"repository_id"`
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`
}

// BenchmarkAuditListJSON measures what AdminAuditHandler.handleList does per
// row: parseDetailsJSON (decode + nested decode), map build, encode. The typed
// variant keeps the details decode and only replaces the outer maps.
func BenchmarkAuditListJSON(b *testing.B) {
	for _, n := range []int{100, 10000} {
		logs := benchAuditLogs(n)

		b.Run(fmt.Sprintf("map/entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &benchResponseWriter{header: http.Header{}}

			for b.Loop() {
				data := make([]any, 0, len(logs))
				for _, log := range logs {
					details := parseDetailsJSON(log.DetailsJSON)
					data = append(data, map[string]any{
						"id":            log.ID,
						"timestamp":     log.CreatedAt,
						"action":        log.Action,
						"actor":         extractActor(details),
						"target":        log.ResourceID,
						"details":       details,
						"ip":            log.IPAddress,
						"repository_id": log.RepositoryID,
						"resource_type": log.ResourceType,
						"resource_id":   log.ResourceID,
					})
				}

				writeJSON(w, http.StatusOK, map[string]any{"data": data, "page": 1, "per_page": n, "total": n})
			}

			b.SetBytes(int64(w.n / b.N))
		})

		b.Run(fmt.Sprintf("typed/entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &benchResponseWriter{header: http.Header{}}

			for b.Loop() {
				data := make([]auditEntryJSON, 0, len(logs))
				for _, log := range logs {
					details := parseDetailsJSON(log.DetailsJSON)
					data = append(data, auditEntryJSON{
						ID: log.ID, Timestamp: log.CreatedAt, Action: log.Action, Actor: extractActor(details),
						Target: log.ResourceID, Details: details, IP: log.IPAddress,
						RepositoryID: log.RepositoryID, ResourceType: log.ResourceType, ResourceID: log.ResourceID,
					})
				}

				writeJSON(w, http.StatusOK, map[string]any{"data": data, "page": 1, "per_page": n, "total": n})
			}

			b.SetBytes(int64(w.n / b.N))
		})
	}
}

func BenchmarkParseDetailsJSON(b *testing.B) {
	raw := benchAuditLogs(1)[0].DetailsJSON

	b.ReportAllocs()

	for b.Loop() {
		_ = parseDetailsJSON(raw)
	}
}
