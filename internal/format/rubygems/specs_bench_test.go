// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

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

type benchGemsDB struct{ *gemsDB }

func (d benchGemsDB) RubyGemsSpecsStamp(_ context.Context, _ string) (string, error) {
	return strconv.Itoa(len(d.gems)), nil
}

// BenchmarkHandleSpecs drives GET /repos/{name}/specs.4.8.gz with an in-memory
// database.
func BenchmarkHandleSpecs(b *testing.B) {
	for _, n := range []int{5000, 50000} {
		db := &gemsDB{DB: formattest.NewDB()}
		for i := range n {
			db.gems = append(db.gems, &models.RubyGem{
				Name: "gem-" + strconv.Itoa(i/5), Version: "1." + strconv.Itoa(i%5) + ".0", Platform: "ruby",
				Description: "A gem", Authors: []string{"Jane"}, Dependencies: []byte(`[{"name":"rake","requirement":">= 0","type":"runtime"}]`),
			})
		}

		h := NewHTTPHandler(formattest.NewLogger(b), benchGemsDB{db}, formattest.NewBlobStore(), "http://localhost")

		router := chi.NewRouter()
		router.Route("/repos/{name}", h.RegisterRepoRoutes)

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/specs.4.8.gz", http.NoBody)

		b.Run(fmt.Sprintf("gems=%d", n), func(b *testing.B) {
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
