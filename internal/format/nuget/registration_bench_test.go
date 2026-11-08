// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// benchPackages builds n versions of one package whose nupkgs carry payload
// bytes of incompressible content, as if they had been published through the
// server (rows carry their dependency groups).
func benchPackages(b *testing.B, n, payload int) (*memDB, *formattest.BlobStore) {
	b.Helper()

	db := &memDB{DB: formattest.NewDB("nuget")}
	bs := formattest.NewBlobStore()

	for i := range n {
		version := fmt.Sprintf("1.0.%d", i)

		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("Contoso.Lib.nuspec")
		_, _ = w.Write([]byte(nuspecFor("Contoso.Lib", version, "bench")))

		pw, _ := zw.Create("lib/net8.0/Contoso.Lib.dll")
		junk := make([]byte, payload)
		_, _ = rand.Read(junk)
		_, _ = pw.Write(junk)
		_ = zw.Close()

		key := fmt.Sprintf("%064x", i)
		bs.Blobs[key] = buf.Bytes()

		pkg := &models.NuGetPackage{
			ID: fmt.Sprintf("id-%d", i), RepositoryID: "repo-1", PackageID: "contoso.lib", Version: version,
			Description: "bench", Authors: "a", BlobKey: key, PackageSize: int64(buf.Len()),
		}
		pkg.DependencyGroups = marshalDependencyGroups(mustNuspec(b, buf.Bytes()).Metadata.Dependencies.DependencyGroups())
		db.pkgs = append(db.pkgs, pkg)
	}

	return db, bs
}

func mustNuspec(b *testing.B, nupkg []byte) *Nuspec {
	b.Helper()

	n, err := parseNuspec(bytes.NewReader(nupkg))
	if err != nil {
		b.Fatal(err)
	}

	return n
}

func benchRegistration(b *testing.B, h *HTTPHandler, router http.Handler, fresh func() (*HTTPHandler, http.Handler)) {
	b.Helper()

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/repos/myrepo/v3/registration/contoso.lib/index.json",
		http.NoBody,
	)
	w := &discardWriter{header: http.Header{}}

	b.ReportAllocs()

	for b.Loop() {
		if fresh != nil {
			h, router = fresh()
		}

		router.ServeHTTP(w, req)
	}

	_ = h

	if w.n == 0 {
		b.Fatal("no body written")
	}
}

func newBenchRouter(b *testing.B, db *memDB, bs *formattest.BlobStore) (*HTTPHandler, http.Handler) {
	b.Helper()

	h := NewHTTPHandler(formattest.NewLogger(b), db, bs, "http://localhost")
	router := chi.NewRouter()
	router.Route("/repos/{name}", h.RegisterRepoRoutes)

	return h, router
}

// BenchmarkRegistrationIndexWarm serves a package with 200 versions from a
// handler that has served it before.
func BenchmarkRegistrationIndexWarm(b *testing.B) {
	db, bs := benchPackages(b, 200, 1024)
	h, router := newBenchRouter(b, db, bs)
	benchRegistration(b, h, router, nil)
}

// BenchmarkRegistrationIndexCold serves a package with 20 versions of 1 MB
// each from a handler that has served nothing yet (a restart, or an evicted
// cache).
func BenchmarkRegistrationIndexCold(b *testing.B) {
	db, bs := benchPackages(b, 20, 1<<20)
	benchRegistration(b, nil, nil, func() (*HTTPHandler, http.Handler) { return newBenchRouter(b, db, bs) })
}

// benchGet serves url repeatedly with the given request headers. A 304 and a
// 200 both count: the point is the cost of the path a restoring client hits.
func benchGet(b *testing.B, router http.Handler, url string, header map[string]string, wantCode int) {
	b.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	w := &codeWriter{}
	w.header = http.Header{}

	b.ReportAllocs()

	for b.Loop() {
		router.ServeHTTP(w, req)
	}

	if w.code != wantCode {
		b.Fatalf("status %d, want %d", w.code, wantCode)
	}
}

type codeWriter struct {
	discardWriter

	code int
}

func (w *codeWriter) WriteHeader(c int) { w.code = c }

func (w *codeWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}

	return w.discardWriter.Write(p)
}

const (
	benchFlatURL = "/repos/myrepo/v3/package/contoso.lib/index.json"
	benchRegURL  = "/repos/myrepo/v3/registration/contoso.lib/index.json"
)

func etagOf(b *testing.B, router http.Handler, url string) string {
	b.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody))

	if rec.Header().Get("ETag") == "" {
		b.Fatal("no ETag")
	}

	return rec.Header().Get("ETag")
}

// The benchmarks below guard the restore hot path: a package of 300 versions
// served fresh (200 with gzip, as clients ask) and revalidated (304).

func BenchmarkRegistration300Hit(b *testing.B) {
	db, bs := benchPackages(b, 300, 256)
	_, router := newBenchRouter(b, db, bs)
	benchGet(b, router, benchRegURL, map[string]string{"Accept-Encoding": "gzip"}, http.StatusOK)
}

func BenchmarkRegistration300NotModified(b *testing.B) {
	db, bs := benchPackages(b, 300, 256)
	_, router := newBenchRouter(b, db, bs)
	benchGet(b, router, benchRegURL, map[string]string{"If-None-Match": etagOf(b, router, benchRegURL)}, http.StatusNotModified)
}

func BenchmarkFlatVersions300Hit(b *testing.B) {
	db, bs := benchPackages(b, 300, 256)
	_, router := newBenchRouter(b, db, bs)
	benchGet(b, router, benchFlatURL, map[string]string{"Accept-Encoding": "gzip"}, http.StatusOK)
}

func BenchmarkFlatVersions300NotModified(b *testing.B) {
	db, bs := benchPackages(b, 300, 256)
	_, router := newBenchRouter(b, db, bs)
	benchGet(b, router, benchFlatURL, map[string]string{"If-None-Match": etagOf(b, router, benchFlatURL)}, http.StatusNotModified)
}
