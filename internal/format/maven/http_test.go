// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type mavenMockDB struct {
	databasetest.BaseMockDB

	repo *models.Repository
	meta *models.RepoMetadata
}

func (m *mavenMockDB) GetRepositoryByName(_ context.Context, _ string) (*models.Repository, error) {
	return m.repo, nil
}

func (m *mavenMockDB) GetRepoMetadataByPath(_ context.Context, _ string, _ string) (*models.RepoMetadata, error) {
	return m.meta, nil
}

type mavenMockBlobStore struct {
	blobs map[string][]byte
}

func (m *mavenMockBlobStore) Put(_ context.Context, data io.Reader, metadata map[string]string) (string, error) {
	b, _ := io.ReadAll(data)
	id := metadata["sha256"]
	m.blobs[id] = b

	return id, nil
}

func (m *mavenMockBlobStore) Get(_ context.Context, id string) (io.ReadCloser, error) {
	b, ok := m.blobs[id]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}

	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *mavenMockBlobStore) Delete(_ context.Context, id string) error {
	delete(m.blobs, id)
	return nil
}

func (m *mavenMockBlobStore) Exists(_ context.Context, id string) (bool, error) {
	_, ok := m.blobs[id]
	return ok, nil
}

func (m *mavenMockBlobStore) GetMetadata(_ context.Context, _ string) (map[string]string, error) {
	return nil, nil
}

func (m *mavenMockBlobStore) List(_ context.Context, _ string, _ int) ([]string, string, error) {
	return nil, "", nil
}

func (m *mavenMockBlobStore) GetSize(_ context.Context, id string) (int64, error) {
	b, ok := m.blobs[id]
	if !ok {
		return 0, io.ErrUnexpectedEOF
	}

	return int64(len(b)), nil
}

func (m *mavenMockBlobStore) ExistsBySHA256(_ context.Context, sha256 string) (string, bool, error) {
	_, ok := m.blobs[sha256]
	return sha256, ok, nil
}

func (m *mavenMockBlobStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil
}

// TestHandleGet_MavenMetadataChecksums verifies that .md5/.sha1/.sha512 for
// maven-metadata.xml are computed from the metadata blob itself rather than
// looked up as a MavenArtifact row.
func TestHandleGet_MavenMetadataChecksums(t *testing.T) {
	xmlData := []byte("<metadata><groupId>com.example</groupId><artifactId>myapp</artifactId></metadata>")

	sha256Hex := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	bs := &mavenMockBlobStore{blobs: map[string][]byte{sha256Hex: xmlData}}
	db := &mavenMockDB{
		repo: &models.Repository{ID: "repo-1", Name: "myrepo"},
		meta: &models.RepoMetadata{
			ID:           "meta-1",
			RepositoryID: "repo-1",
			Path:         "com/example/myapp/maven-metadata.xml",
			BlobSHA256:   sha256Hex,
			BlobSize:     int64(len(xmlData)),
		},
	}

	log, err := logger.New("info", "json", "stdout")
	if err != nil {
		t.Fatalf("logger.New: %v", err)
	}

	h := NewHTTPHandler(log, db, bs)
	router := chi.NewRouter()
	h.RegisterRoutes(router, "/repos/{name}")

	md5Sum := md5.Sum(xmlData)
	sha1Sum := sha1.Sum(xmlData)
	sha512Sum := sha512.Sum512(xmlData)

	cases := []struct {
		suffix string
		want   string
	}{
		{".md5", hex.EncodeToString(md5Sum[:])},
		{".sha1", hex.EncodeToString(sha1Sum[:])},
		{".sha256", sha256Hex},
		{".sha512", hex.EncodeToString(sha512Sum[:])},
	}

	for _, tc := range cases {
		t.Run(tc.suffix, func(t *testing.T) {
			path := "/repos/myrepo/com/example/myapp/maven-metadata.xml" + tc.suffix
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}

			body := w.Body.String()
			if len(body) < len(tc.want) || body[:len(tc.want)] != tc.want {
				t.Errorf("body = %q, want prefix %q", body, tc.want)
			}
		})
	}
}

type mavenQuarantineDB struct {
	mavenMockDB

	art         *models.MavenArtifact
	quarantined bool
}

func (m *mavenQuarantineDB) GetMavenArtifactByPath(_ context.Context, _, _ string) (*models.MavenArtifact, error) {
	return m.art, nil
}

func (m *mavenQuarantineDB) IsArtifactQuarantined(_ context.Context, _ string, _ []string) (bool, error) {
	return m.quarantined, nil
}

func TestHandleGet_Quarantine(t *testing.T) {
	const sum = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	for _, quarantined := range []bool{true, false} {
		bs := &mavenMockBlobStore{blobs: map[string][]byte{sum: []byte("jar")}}
		db := &mavenQuarantineDB{
			art:         &models.MavenArtifact{BlobID: "uuid", SHA256: sum, RepoPath: "com/example/a/1.0/a-1.0.jar"},
			quarantined: quarantined,
		}
		db.repo = &models.Repository{ID: "repo-1", Name: "myrepo"}

		log, _ := logger.New("info", "json", "stdout")
		router := chi.NewRouter()
		NewHTTPHandler(log, db, bs).RegisterRoutes(router, "/repos/{name}")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/com/example/a/1.0/a-1.0.jar", nil))

		want := http.StatusOK
		if quarantined {
			want = http.StatusForbidden
		}

		if w.Code != want {
			t.Errorf("quarantined=%v: status = %d, want %d", quarantined, w.Code, want)
		}
	}
}
