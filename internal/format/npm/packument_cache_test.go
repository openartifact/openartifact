// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// packumentDB adds dist-tags to the tarball fake.
type packumentDB struct{ *tarballMockDB }

func (packumentDB) GetNPMDistTags(context.Context, string) (map[string]string, error) {
	return map[string]string{"latest": "1.0.0"}, nil
}

func newPackumentRouter(t *testing.T, db *tarballMockDB, bs *tarballMockBlobStore) http.Handler {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), packumentDB{db}, bs, "http://localhost:8080")
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

	return router
}

func newPackumentDB(updated time.Time, versions ...string) *tarballMockDB {
	db := &tarballMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "myrepo"},
		pkg:  &models.NPMPackage{ID: "pkg-1", FullName: "lodash", UpdatedAt: updated},
	}

	for _, v := range versions {
		db.versions = append(db.versions, &models.NPMVersion{
			ID: "v-" + v, PackageID: "pkg-1", Version: v, TarballFilename: "lodash-" + v + ".tgz",
			ManifestJSON: []byte(`{"name":"lodash","version":"` + v + `"}`),
		})
	}

	return db
}

func getPackument(router http.Handler, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/lodash", http.NoBody)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w
}

func TestPackumentRenderedOncePerStamp(t *testing.T) {
	db := newPackumentDB(time.Unix(1700000000, 0), "1.0.0")
	router := newPackumentRouter(t, db, &tarballMockBlobStore{})

	a, b := getPackument(router, nil), getPackument(router, nil)
	if a.Code != http.StatusOK || !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Fatalf("status %d, bodies equal %v", a.Code, bytes.Equal(a.Body.Bytes(), b.Body.Bytes()))
	}

	if db.listCalls != 1 {
		t.Errorf("versions loaded %d times for 2 requests, want 1", db.listCalls)
	}

	// The abbreviated document is a different representation under a different
	// validator and must not be served from the full one's cache entry.
	abbr := getPackument(router, map[string]string{"Accept": "application/vnd.npm.install-v1+json"})
	if abbr.Header().Get("ETag") == a.Header().Get("ETag") || bytes.Equal(abbr.Body.Bytes(), a.Body.Bytes()) {
		t.Error("abbreviated and full packuments share a cache entry")
	}
}

// Publishing, dist-tag changes and deletions mark the package row stale, which
// moves the validator: the next request renders the new state.
func TestPackumentFollowsStaleMarker(t *testing.T) {
	db := newPackumentDB(time.Unix(1700000000, 0), "1.0.0")
	router := newPackumentRouter(t, db, &tarballMockBlobStore{})

	first := getPackument(router, nil)
	etag := first.Header().Get("ETag")

	db.versions = append(db.versions, newPackumentDB(time.Time{}, "2.0.0").versions...)
	db.pkg.UpdatedAt = time.Unix(1700000100, 0)

	updated := getPackument(router, map[string]string{"If-None-Match": etag})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"2.0.0"`) || updated.Header().Get("ETag") == etag {
		t.Fatalf("after publish: status %d etag %q", updated.Code, updated.Header().Get("ETag"))
	}

	db.versions = db.versions[:1]
	db.pkg.UpdatedAt = time.Unix(1700000200, 0)

	if removed := getPackument(router, nil); strings.Contains(removed.Body.String(), `"2.0.0"`) {
		t.Error("deleted version still in the packument")
	}
}

func TestPackumentGzip(t *testing.T) {
	versions := make([]string, 0, 100)
	for i := range 100 {
		versions = append(versions, "1.0."+strconv.Itoa(i))
	}

	router := newPackumentRouter(t, newPackumentDB(time.Unix(1700000000, 0), versions...), &tarballMockBlobStore{})

	plain := getPackument(router, nil)
	zipped := getPackument(router, map[string]string{"Accept-Encoding": "gzip"})

	if plain.Header().Get("Content-Encoding") != "" || zipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("encodings: plain %q, gzip %q", plain.Header().Get("Content-Encoding"), zipped.Header().Get("Content-Encoding"))
	}

	zr, err := gzip.NewReader(zipped.Body)
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := io.ReadAll(zr); !bytes.Equal(got, plain.Body.Bytes()) {
		t.Error("decompressed packument differs")
	}
}

type sizeCountingStore struct {
	*tarballMockBlobStore

	sizes atomic.Int32
}

func (s *sizeCountingStore) GetSize(ctx context.Context, id string) (int64, error) {
	s.sizes.Add(1)

	return s.tarballMockBlobStore.GetSize(ctx, id)
}

func TestTarballSizeComesFromDatabase(t *testing.T) {
	payload := []byte("tarball-bytes")
	bs := &sizeCountingStore{tarballMockBlobStore: &tarballMockBlobStore{blobs: map[string][]byte{"sha-abc": payload}}}

	for _, tc := range []struct {
		name      string
		dbSize    int64
		wantStats int32
	}{
		{"recorded size", int64(len(payload)), 0},
		{"unrecorded size asks the store", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bs.sizes.Store(0)

			db := newPackumentDB(time.Time{}, "1.0.0")
			db.versions[0].BlobSHA256, db.versions[0].BlobSizeBytes = "sha-abc", tc.dbSize

			h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost:8080")
			router := chi.NewRouter()
			router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.NoBody))

			if w.Code != http.StatusOK || w.Header().Get("Content-Length") != strconv.Itoa(len(payload)) || !bytes.Equal(w.Body.Bytes(), payload) {
				t.Fatalf("status %d, Content-Length %q, body %q", w.Code, w.Header().Get("Content-Length"), w.Body.String())
			}

			if got := bs.sizes.Load(); got != tc.wantStats {
				t.Errorf("blob store size lookups = %d, want %d", got, tc.wantStats)
			}
		})
	}
}
