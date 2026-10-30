// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// chartDigest is a sha256-looking digest for chart i.
func chartDigest(i int) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(i)))

	return hex.EncodeToString(sum[:])
}

// benchCharts builds n charts spread over n/10 chart names (10 versions each),
// like a repository that keeps release history.
func benchCharts(n int) []*models.HelmChart {
	charts := make([]*models.HelmChart, n)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for i := range charts {
		name := "chart-" + strconv.Itoa(i/10)

		charts[i] = &models.HelmChart{
			ID:          strconv.Itoa(i),
			Name:        name,
			Version:     "1." + strconv.Itoa(i%10) + ".0",
			AppVersion:  "2." + strconv.Itoa(i%10) + ".1",
			Description: "A Helm chart for " + name,
			Keywords:    []string{"app", "web", "example"},
			Home:        "https://example.org/" + name,
			Sources:     []string{"https://example.org/src/" + name},
			Maintainers: []map[string]string{{"name": "Jane", "email": "jane@example.org"}},
			Icon:        "https://example.org/icon.png",
			APIVersion:  "v2",
			Type:        "application",
			Digest:      chartDigest(i),
			SizeBytes:   12345,
			CreatedAt:   created,
		}
	}

	return charts
}

// BenchmarkHelmIndexYAML measures renderHelmIndex, the full cost of rendering
// index.yaml once the charts are loaded. BenchmarkHelmIndexYAMLWholeDocument is
// its baseline: the same index marshalled as one yaml document.
func BenchmarkHelmIndexYAML(b *testing.B) {
	benchHelmIndexRender(b, renderHelmIndex)
}

func BenchmarkHelmIndexYAMLWholeDocument(b *testing.B) {
	benchHelmIndexRender(b, wholeDocumentHelmIndex)
}

func benchHelmIndexRender(b *testing.B, render func([]*models.HelmChart, time.Time) ([]byte, error)) {
	b.Helper()

	generated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, n := range []int{100, 1000, 10000} {
		charts := benchCharts(n)

		b.Run(fmt.Sprintf("charts=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			var size int

			for b.Loop() {
				out, err := render(charts, generated)
				if err != nil {
					b.Fatal(err)
				}

				size = len(out)
			}

			b.SetBytes(int64(size))
		})
	}
}

type benchHelmDB struct {
	*formattest.DB

	charts []*models.HelmChart
}

func (d *benchHelmDB) ListHelmCharts(_ context.Context, _ string) ([]*models.HelmChart, error) {
	return d.charts, nil
}

func (d *benchHelmDB) HelmIndexStamp(_ context.Context, _ string) (string, error) {
	return strconv.Itoa(len(d.charts)), nil
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

// BenchmarkHandleGetIndex drives GET /repos/{name}/index.yaml through chi with
// an in-memory database.
func BenchmarkHandleGetIndex(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		db := &benchHelmDB{DB: formattest.NewDB(), charts: benchCharts(n)}
		h := NewHTTPHandler(formattest.NewLogger(b), db, formattest.NewBlobStore(), "http://localhost:8080")

		router := chi.NewRouter()
		router.Route("/repos/{name}", h.RegisterRepoRoutes)

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/index.yaml", http.NoBody)

		b.Run(fmt.Sprintf("charts=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			w := &discardResponseWriter{header: http.Header{}}

			for b.Loop() {
				router.ServeHTTP(w, req)
			}

			if w.n == 0 {
				b.Fatal("no body written")
			}
		})
	}
}
