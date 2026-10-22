// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/presign"
)

type redirectingStore struct {
	*mavenMockBlobStore

	got presign.Blob
}

func (r *redirectingStore) TryRedirect(w http.ResponseWriter, _ *http.Request, blob presign.Blob) bool {
	r.got = blob
	w.WriteHeader(http.StatusTemporaryRedirect)

	return true
}

// The artifact row already knows the blob size; the presign path must be
// handed it instead of asking the object store for it.
func TestDownloadPresignUsesStoredSize(t *testing.T) {
	const sum = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	store := &redirectingStore{mavenMockBlobStore: &mavenMockBlobStore{blobs: map[string][]byte{sum: []byte("jar")}}}
	db := &mavenQuarantineDB{art: &models.MavenArtifact{BlobID: "uuid", SHA256: sum, BlobSizeBytes: 9999, RepoPath: "com/example/a/1.0/a-1.0.jar"}}
	db.repo = &models.Repository{ID: "repo-1", Name: "myrepo"}

	router := chi.NewRouter()
	NewHTTPHandler(formattest.NewLogger(t), db, store).RegisterRoutes(router, "/repos/{name}")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/myrepo/com/example/a/1.0/a-1.0.jar", http.NoBody))

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", w.Code)
	}

	if store.got.SHA256 != sum || store.got.Size != 9999 {
		t.Errorf("presign.Blob = {SHA256 %q, Size %d}, want {%s, 9999}", store.got.SHA256, store.got.Size, sum)
	}
}
