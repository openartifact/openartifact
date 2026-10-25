// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// tarballMockDB provides just enough of database.Database for handleTarball.
type tarballMockDB struct {
	databasetest.BaseMockDB

	repo     *models.Repository
	pkg      *models.NPMPackage
	versions []*models.NPMVersion

	// versionLookups counts single-version lookups (the cheap tarball path).
	versionLookups int
	// listCalls counts full version-list loads.
	listCalls int

	// quarantined lists the artifact keys IsArtifactQuarantined reports as quarantined.
	quarantined map[string]bool
}

func (m *tarballMockDB) IsArtifactQuarantined(_ context.Context, _ string, keys []string) (bool, error) {
	for _, k := range keys {
		if m.quarantined[k] {
			return true, nil
		}
	}

	return false, nil
}

func (m *tarballMockDB) GetRepositoryByName(_ context.Context, _ string) (*models.Repository, error) {
	return m.repo, nil
}

func (m *tarballMockDB) GetNPMPackageByName(_ context.Context, _, _ string) (*models.NPMPackage, error) {
	return m.pkg, nil
}

func (m *tarballMockDB) GetNPMVersionsByPackage(_ context.Context, _ string) ([]*models.NPMVersion, error) {
	m.listCalls++

	return m.versions, nil
}

func (m *tarballMockDB) GetNPMVersion(_ context.Context, _, version string) (*models.NPMVersion, error) {
	m.versionLookups++

	for _, v := range m.versions {
		if v.Version == version {
			return v, nil
		}
	}

	return nil, nil
}

// tarballMockBlobStore is a minimal content-addressed in-memory blob store.
type tarballMockBlobStore struct {
	blobs map[string][]byte
}

func (m *tarballMockBlobStore) Put(_ context.Context, data io.Reader, metadata map[string]string) (string, error) {
	b, _ := io.ReadAll(data)
	id := metadata["sha256"]
	m.blobs[id] = b

	return id, nil
}

func (m *tarballMockBlobStore) Get(_ context.Context, id string) (io.ReadCloser, error) {
	b, ok := m.blobs[id]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}

	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *tarballMockBlobStore) Delete(_ context.Context, id string) error {
	delete(m.blobs, id)
	return nil
}

func (m *tarballMockBlobStore) Exists(_ context.Context, id string) (bool, error) {
	_, ok := m.blobs[id]
	return ok, nil
}

func (m *tarballMockBlobStore) GetMetadata(_ context.Context, _ string) (map[string]string, error) {
	return nil, nil
}

func (m *tarballMockBlobStore) List(_ context.Context, _ string, _ int) ([]string, string, error) {
	return nil, "", nil
}

func (m *tarballMockBlobStore) GetSize(_ context.Context, id string) (int64, error) {
	b, ok := m.blobs[id]
	if !ok {
		return 0, io.ErrUnexpectedEOF
	}

	return int64(len(b)), nil
}

func (m *tarballMockBlobStore) ExistsBySHA256(_ context.Context, sha256 string) (string, bool, error) {
	_, ok := m.blobs[sha256]
	return sha256, ok, nil
}

func (m *tarballMockBlobStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil
}

// TestHandleTarball_ContentLengthMatchesBody verifies that the handler sets
// Content-Length to the actual blob size.
func TestHandleTarball_ContentLengthMatchesBody(t *testing.T) {
	payload := []byte("this is definitely not zero bytes of tarball content")

	bs := &tarballMockBlobStore{blobs: map[string][]byte{"sha-abc": payload}}
	db := &tarballMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "myrepo"},
		pkg:  &models.NPMPackage{ID: "pkg-1", FullName: "lodash"},
		versions: []*models.NPMVersion{
			{ID: "v1", PackageID: "pkg-1", Version: "1.0.0", BlobID: "uuid-1", BlobSHA256: "sha-abc", TarballFilename: "lodash-1.0.0.tgz"},
		},
	}

	log, err := logger.New("info", "json", "stdout")
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}

	h := NewHTTPHandler(log, db, bs, "http://localhost:8080")
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		h.RegisterRepoRoutes(r)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	wantLen := strconv.Itoa(len(payload))
	if got := w.Header().Get("Content-Length"); got != wantLen {
		t.Errorf("Content-Length header = %q, want %q", got, wantLen)
	}

	if w.Body.Len() != len(payload) {
		t.Errorf("body length = %d, want %d", w.Body.Len(), len(payload))
	}

	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Error("body content does not match stored blob")
	}
}

// TestHandleTarball_Quarantine verifies a quarantined tarball is refused with
// 403 under every identifier it is known by, while others stay downloadable.
func TestHandleTarball_Quarantine(t *testing.T) {
	cases := []struct {
		name string
		key  string
		path string
		want int
	}{
		{"by name@version", "lodash@1.0.0", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusForbidden},
		{"by package path", "lodash/-/lodash-1.0.0.tgz", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusForbidden},
		{"by filename", "lodash-1.0.0.tgz", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusForbidden},
		{"by request path", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusForbidden},
		{"other version quarantined", "lodash@2.0.0", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusOK},
		{"nothing quarantined", "", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs := &tarballMockBlobStore{blobs: map[string][]byte{"sha-abc": []byte("payload")}}
			db := &tarballMockDB{
				repo: &models.Repository{ID: "repo-1", Name: "myrepo"},
				pkg:  &models.NPMPackage{ID: "pkg-1", FullName: "lodash"},
				versions: []*models.NPMVersion{
					{ID: "v1", PackageID: "pkg-1", Version: "1.0.0", BlobSHA256: "sha-abc", TarballFilename: "lodash-1.0.0.tgz"},
				},
				quarantined: map[string]bool{tc.key: tc.key != ""},
			}

			log, err := logger.New("info", "json", "stdout")
			if err != nil {
				t.Fatalf("logger.New: %v", err)
			}

			h := NewHTTPHandler(log, db, bs, "http://localhost:8080")
			router := chi.NewRouter()
			router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.want, w.Body.String())
			}

			if tc.want == http.StatusForbidden && w.Body.Len() > 0 && bytes.Contains(w.Body.Bytes(), []byte("payload")) {
				t.Error("quarantined response leaked artifact bytes")
			}
		})
	}
}

// TestHandleTarball_Resolution covers how a tarball filename is mapped to its
// version: the conventional name uses one version lookup, an unconventional
// name still resolves through the full version list, and an unknown one is a 404.
func TestHandleTarball_Resolution(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		stored      string
		pkg         string
		path        string
		want        int
		wantLookups int
	}{
		{"conventional", "lodash-1.0.0.tgz", "lodash-1.0.0.tgz", "lodash", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusOK, 1},
		{"scoped", "core-1.0.0.tgz", "core-1.0.0.tgz", "@acme/core", "/repos/myrepo/@acme/core/-/core-1.0.0.tgz", http.StatusOK, 1},
		{"unconventional stored name", "lodash-1.0.0.tgz", "custom-name.tgz", "lodash", "/repos/myrepo/lodash/-/custom-name.tgz", http.StatusOK, 0},
		{"unknown version", "lodash-1.0.0.tgz", "lodash-1.0.0.tgz", "lodash", "/repos/myrepo/lodash/-/lodash-9.9.9.tgz", http.StatusNotFound, 1},
		{"stored filename differs from the derived one", "lodash-1.0.0.tgz", "other.tgz", "lodash", "/repos/myrepo/lodash/-/lodash-1.0.0.tgz", http.StatusNotFound, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs := &tarballMockBlobStore{blobs: map[string][]byte{"sha-abc": []byte("data")}}
			db := &tarballMockDB{
				repo:     &models.Repository{ID: "repo-1", Name: "myrepo"},
				pkg:      &models.NPMPackage{ID: "pkg-1", FullName: tc.pkg},
				versions: []*models.NPMVersion{{ID: "v1", PackageID: "pkg-1", Version: "1.0.0", BlobSHA256: "sha-abc", TarballFilename: tc.stored}},
			}

			log, err := logger.New("info", "json", "stdout")
			if err != nil {
				t.Fatalf("logger.New: %v", err)
			}

			h := NewHTTPHandler(log, db, bs, "http://localhost:8080")
			router := chi.NewRouter()
			router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, nil))

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.want, w.Body.String())
			}

			if tc.wantLookups == 1 && db.versionLookups != 1 {
				t.Errorf("versionLookups = %d, want 1", db.versionLookups)
			}
		})
	}
}

// TestHandlePackument_ConditionalSkipsVersionLoad verifies a matching
// If-None-Match is answered with 304 before any version is loaded.
func TestHandlePackument_ConditionalSkipsVersionLoad(t *testing.T) {
	updated := time.Unix(1700000000, 0)
	db := &tarballMockDB{
		repo:     &models.Repository{ID: "repo-1", Name: "myrepo"},
		pkg:      &models.NPMPackage{ID: "pkg-1", FullName: "lodash", UpdatedAt: updated},
		versions: []*models.NPMVersion{{ID: "v1", PackageID: "pkg-1", Version: "1.0.0", TarballFilename: "lodash-1.0.0.tgz"}},
	}

	log, err := logger.New("info", "json", "stdout")
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}

	h := NewHTTPHandler(log, db, &tarballMockBlobStore{}, "http://localhost:8080")
	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) { h.RegisterRepoRoutes(r) })

	etag := fmt.Sprintf(`"%x"`, updated.UnixNano())
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/lodash", nil)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w.Code)
	}

	if db.listCalls != 0 {
		t.Errorf("versions loaded %d times for a 304, want 0", db.listCalls)
	}

	if got := w.Header().Get("ETag"); got != etag {
		t.Errorf("ETag = %q, want %q", got, etag)
	}
}
