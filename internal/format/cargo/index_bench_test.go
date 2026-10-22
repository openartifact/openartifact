// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// benchCargoDB answers the stamp in O(1) so the benchmark measures the
// handler, not the fake.
type benchCargoDB struct{ *cargoDB }

func (d benchCargoDB) CargoIndexStamp(_ context.Context, _, _ string) (string, error) {
	return strconv.Itoa(len(d.crates)), nil
}

// BenchmarkHandleSparseIndex drives GET /repos/{name}/index/.../{crate} for a
// crate whose publish metadata carries a 20 KB readme per version.
func BenchmarkHandleSparseIndex(b *testing.B) {
	meta := `{"deps":[{"name":"serde","version_req":"^1","features":["derive"],"optional":false,"default_features":true,"target":null,"kind":"normal"}],` +
		`"features":{"default":["a"],"a":[]},"links":null,"readme":"` + strings.Repeat("x", 20000) + `"}`

	for _, n := range []int{50, 500} {
		db := &cargoDB{DB: formattest.NewDB()}
		for i := range n {
			db.crates = append(db.crates, &models.CargoCrate{Name: "demo", Version: "0.1." + strconv.Itoa(i), Checksum: strings.Repeat("a", 64), MetadataJSON: []byte(meta)})
		}

		h := NewHTTPHandler(formattest.NewLogger(b), benchCargoDB{db}, formattest.NewBlobStore(), "http://localhost")

		router := chi.NewRouter()
		router.Route("/repos/{name}", h.RegisterRepoRoutes)

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/index/de/mo/demo", http.NoBody)

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
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
