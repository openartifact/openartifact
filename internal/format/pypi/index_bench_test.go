// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// benchPyPIDB serves fixed rows; its stamps are constant-time, so the
// benchmarks measure the handler, not the database.
type benchPyPIDB struct {
	*formattest.DB

	all      []*models.PyPIPackage
	versions []*models.PyPIPackage
}

func (d *benchPyPIDB) ListPyPIPackageNames(_ context.Context, _ string) ([]string, error) {
	return distinctPyPINames(d.all), nil
}

func (d *benchPyPIDB) ListPyPIPackageVersions(_ context.Context, _, _ string) ([]*models.PyPIPackage, error) {
	return d.versions, nil
}

func (d *benchPyPIDB) PyPIIndexStamp(_ context.Context, _ string) (string, error) {
	return strconv.Itoa(len(d.all)), nil
}

func (d *benchPyPIDB) PyPIProjectStamp(_ context.Context, _, _ string) (string, int64, error) {
	return strconv.Itoa(len(d.versions)), int64(len(d.versions)), nil
}

func benchPyPIFile(project, version, ext string) *models.PyPIPackage {
	return &models.PyPIPackage{
		Name:           project,
		Version:        version,
		Filename:       project + "-" + version + ext,
		RequiresPython: ">=3.9",
		Summary:        "An example project",
		Author:         "Jane",
		AuthorEmail:    "jane@example.org",
		License:        "MIT",
		MD5Digest:      fmt.Sprintf("%032x", len(project)+len(version)),
		SHA256Digest:   fmt.Sprintf("%064x", len(project)+len(version)),
	}
}

// benchPyPIProjects returns n files spread over n/2 projects (wheel + sdist
// each, mixed-case names with separators so normalisation does real work).
func benchPyPIProjects(n int) []*models.PyPIPackage {
	files := make([]*models.PyPIPackage, 0, n)

	for i := 0; len(files) < n; i++ {
		project := "My_Example.Project" + strconv.Itoa(i)

		files = append(files,
			benchPyPIFile(project, "1.0."+strconv.Itoa(i%10), "-py3-none-any.whl"),
			benchPyPIFile(project, "1.0."+strconv.Itoa(i%10), ".tar.gz"),
		)
	}

	return files[:n]
}

// benchPyPIVersions returns n files of one project: n/2 releases, wheel+sdist.
func benchPyPIVersions(n int) []*models.PyPIPackage {
	files := make([]*models.PyPIPackage, 0, n)

	for i := 0; len(files) < n; i++ {
		v := "1." + strconv.Itoa(i/100) + "." + strconv.Itoa(i%100)

		files = append(files,
			benchPyPIFile("demo", v, "-py3-none-any.whl"),
			benchPyPIFile("demo", v, ".tar.gz"),
		)
	}

	return files[:n]
}

type discardResponseWriter struct {
	header http.Header
	n      int
}

func (w *discardResponseWriter) Header() http.Header { return w.header }
func (w *discardResponseWriter) WriteHeader(int)     {}
func (w *discardResponseWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

// benchServe measures the three requests a client makes against one document:
// a plain GET, a GET accepting gzip (what pip sends) and a revalidation that
// is answered 304. The response headers are reset per request, as a real
// connection does.
func benchServe(b *testing.B, db *benchPyPIDB, target string) {
	b.Helper()

	h := NewHTTPHandler(formattest.NewLogger(b), db, formattest.NewBlobStore(), "http://localhost:8080")

	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	probe := httptest.NewRecorder()
	router.ServeHTTP(probe, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody))

	etag := probe.Header().Get("ETag")
	if probe.Code != http.StatusOK || etag == "" {
		b.Fatalf("probe: status %d, ETag %q", probe.Code, etag)
	}

	for _, tc := range []struct {
		name   string
		header map[string]string
	}{
		{"identity", nil},
		{"gzip", map[string]string{"Accept-Encoding": "gzip"}},
		{"304", map[string]string{"If-None-Match": etag}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}

			w := &discardResponseWriter{header: http.Header{}}

			b.ReportAllocs()

			for b.Loop() {
				clear(w.header)
				router.ServeHTTP(w, req)
			}

			if w.n == 0 && tc.name != "304" {
				b.Fatal("no body written")
			}
		})
	}
}

// BenchmarkHandleSimpleIndex serves the project list (GET /simple/) of
// repositories with 200, 2000 and 10000 projects.
func BenchmarkHandleSimpleIndex(b *testing.B) {
	for _, projects := range []int{200, 2000, 10000} {
		db := &benchPyPIDB{DB: formattest.NewDB("pypi"), all: benchPyPIProjects(projects * 2)}

		b.Run(fmt.Sprintf("projects=%d", projects), func(b *testing.B) {
			benchServe(b, db, "/repos/myrepo/simple/")
		})
	}
}

// BenchmarkHandlePackageIndex serves one project's file list (GET
// /simple/{pkg}/) of a project with 10, 300 and 2000 releases (a wheel and an
// sdist each).
func BenchmarkHandlePackageIndex(b *testing.B) {
	for _, releases := range []int{10, 300, 2000} {
		db := &benchPyPIDB{DB: formattest.NewDB("pypi"), versions: benchPyPIVersions(releases * 2)}

		b.Run(fmt.Sprintf("releases=%d", releases), func(b *testing.B) {
			benchServe(b, db, "/repos/myrepo/simple/demo/")
		})
	}
}

// BenchmarkHandleJSON serves the JSON API document of one project (GET
// /json/{pkg}) with the same release counts.
func BenchmarkHandleJSON(b *testing.B) {
	for _, releases := range []int{10, 300, 2000} {
		db := &benchPyPIDB{DB: formattest.NewDB("pypi"), versions: benchPyPIVersions(releases * 2)}

		b.Run(fmt.Sprintf("releases=%d", releases), func(b *testing.B) {
			benchServe(b, db, "/repos/myrepo/json/demo")
		})
	}
}
