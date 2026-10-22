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

type benchPyPIDB struct {
	*formattest.DB

	all      []*models.PyPIPackage
	versions []*models.PyPIPackage
}

func (d *benchPyPIDB) ListPyPIPackages(_ context.Context, _ string) ([]*models.PyPIPackage, error) {
	return d.all, nil
}

func (d *benchPyPIDB) ListPyPIPackageNames(_ context.Context, _ string) ([]string, error) {
	return distinctPyPINames(d.all), nil
}

func (d *benchPyPIDB) ListPyPIPackageVersions(_ context.Context, _, _ string) ([]*models.PyPIPackage, error) {
	return d.versions, nil
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

func benchServe(b *testing.B, db *benchPyPIDB, target string) {
	b.Helper()

	h := NewHTTPHandler(formattest.NewLogger(b), db, formattest.NewBlobStore(), "http://localhost:8080")

	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
	w := &discardResponseWriter{header: http.Header{}}

	b.ReportAllocs()

	for b.Loop() {
		router.ServeHTTP(w, req)
	}

	if w.n == 0 {
		b.Fatal("no body written")
	}
}

// BenchmarkHandleSimpleIndex renders the project list (GET /simple/).
func BenchmarkHandleSimpleIndex(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		db := &benchPyPIDB{DB: formattest.NewDB(), all: benchPyPIProjects(n)}

		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			benchServe(b, db, "/repos/myrepo/simple/")
		})
	}
}

// BenchmarkHandlePackageIndex renders one project's file list (GET /simple/{pkg}/).
func BenchmarkHandlePackageIndex(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		db := &benchPyPIDB{DB: formattest.NewDB(), versions: benchPyPIVersions(n)}

		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			benchServe(b, db, "/repos/myrepo/simple/demo/")
		})
	}
}

// BenchmarkHandleJSON renders the JSON API document of one project.
func BenchmarkHandleJSON(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		db := &benchPyPIDB{DB: formattest.NewDB(), versions: benchPyPIVersions(n)}

		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			benchServe(b, db, "/repos/myrepo/json/demo")
		})
	}
}

func BenchmarkNormalizePyPIName(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		_ = normalizePyPIName("My_Example.Project_Name")
	}
}
