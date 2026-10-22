// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

type benchPackumentDB struct{ *tarballMockDB }

func (benchPackumentDB) GetNPMDistTags(context.Context, string) (map[string]string, error) {
	return map[string]string{"latest": "0.0.0"}, nil
}

type discardWriter struct {
	header http.Header
	n      int
}

func (w *discardWriter) Header() http.Header { return w.header }
func (w *discardWriter) WriteHeader(int)     {}
func (w *discardWriter) Write(p []byte) (int, error) {
	w.n += len(p)

	return len(p), nil
}

// BenchmarkHandlePackument drives GET /repos/{name}/{pkg} (a changed-ETag
// request: the validator is not sent) with an in-memory database.
func BenchmarkHandlePackument(b *testing.B) {
	for _, n := range []int{100, 1000} {
		db := &tarballMockDB{
			repo:     &models.Repository{ID: "repo-1", Name: "myrepo"},
			pkg:      &models.NPMPackage{ID: "pkg-1", FullName: "example", UpdatedAt: time.Unix(1700000000, 0)},
			versions: benchNPMVersions(n),
		}

		h := NewHTTPHandler(formattest.NewLogger(b), benchPackumentDB{db}, &tarballMockBlobStore{}, "http://localhost:8080")
		router := chi.NewRouter()
		router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/example", http.NoBody)

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &discardWriter{header: http.Header{}}

			for b.Loop() {
				router.ServeHTTP(w, req)
			}

			if w.n < n*100 {
				b.Fatalf("wrote only %d bytes", w.n)
			}
		})
	}
}
