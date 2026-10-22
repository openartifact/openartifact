// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

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

// benchCondaDB answers the stamp in O(1) so the benchmark measures the
// handler, not the fake.
type benchCondaDB struct{ *condaDB }

func (d benchCondaDB) CondaRepodataStamp(_ context.Context, _, _ string) (string, error) {
	return strconv.Itoa(len(d.pkgs)), nil
}

// BenchmarkHandleRepodata drives GET /repos/{name}/{platform}/repodata.json
// through chi with an in-memory database.
func BenchmarkHandleRepodata(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		db := &condaDB{DB: formattest.NewDB()}
		for i := range n {
			addPkg(db, i)
		}

		h := NewHTTPHandler(formattest.NewLogger(b), benchCondaDB{db}, formattest.NewBlobStore(), "http://localhost")

		router := chi.NewRouter()
		router.Route("/repos/{name}", h.RegisterRepoRoutes)

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/noarch/repodata.json", http.NoBody)

		b.Run(fmt.Sprintf("packages=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &discardWriter{header: http.Header{}}

			for b.Loop() {
				router.ServeHTTP(w, req)
			}

			if w.n == 0 {
				b.Fatal("no body written")
			}
		})
	}
}
