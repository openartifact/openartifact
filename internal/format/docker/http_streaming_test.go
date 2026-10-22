// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeDB satisfies database.Database by embedding the interface; only the
// handful of methods used by the blob-upload paths are implemented. Any other
// call panics, which keeps the fake honest about what the handlers touch.
type fakeDB struct {
	database.Database

	repo   *models.Repository
	upload *models.DockerUpload

	offset        int64
	createdBlob   *models.DockerBlob
	blobCreates   int
	blobs         map[string]*models.DockerBlob // digest -> blob held by repo
	deletedUpload bool

	ledger        quotaLedger // quota reservations (see quota_test.go)
	createBlobErr error       // fails CreateDockerBlob
}

func (f *fakeDB) GetRepositoryByName(_ context.Context, _ string) (*models.Repository, error) {
	return f.repo, nil
}

func (f *fakeDB) uploadMatches(repositoryID, uploadUUID string) bool {
	if f.upload == nil {
		return false
	}

	if f.upload.RepositoryID != "" {
		if f.upload.RepositoryID != repositoryID {
			return false
		}
	} else if f.repo != nil && f.repo.ID != repositoryID {
		return false
	}

	if f.upload.UploadUUID != "" && f.upload.UploadUUID != uploadUUID {
		return false
	}

	return true
}

func (f *fakeDB) GetDockerUpload(_ context.Context, repositoryID, uploadUUID string) (*models.DockerUpload, error) {
	if !f.uploadMatches(repositoryID, uploadUUID) {
		return nil, nil
	}

	return f.upload, nil
}

func (f *fakeDB) UpdateDockerUploadOffset(_ context.Context, repositoryID, uploadUUID string, oldOffset, newOffset int64) (bool, error) {
	if !f.uploadMatches(repositoryID, uploadUUID) || f.upload.Offset != oldOffset {
		return false, nil
	}

	f.offset = newOffset
	f.upload.Offset = newOffset

	return true, nil
}

func (f *fakeDB) GetDockerBlob(_ context.Context, _, digest string) (*models.DockerBlob, error) {
	return f.blobs[digest], nil
}

func (f *fakeDB) CreateDockerBlob(_ context.Context, blob *models.DockerBlob) error {
	if f.createBlobErr != nil {
		return f.createBlobErr
	}

	f.createdBlob = blob
	f.blobCreates++
	blob.Inserted = true

	if f.blobs == nil {
		f.blobs = map[string]*models.DockerBlob{}
	}

	f.blobs[blob.Digest] = blob

	return nil
}

func (f *fakeDB) DeleteDockerUpload(_ context.Context, repositoryID, uploadUUID string) error {
	if !f.uploadMatches(repositoryID, uploadUUID) {
		return nil
	}

	f.deletedUpload = true
	return nil
}

func newTestHandler(t *testing.T, db database.Database) (*HTTPHandler, storage.BlobStore) {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	return NewHTTPHandler(log, db, store, "http://localhost"), store
}

// withChiVars attaches chi URL params so urlparams.Vars resolves them.
func withChiVars(r *http.Request, kv map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range kv {
		rctx.URLParams.Add(k, v)
	}

	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func tempUploadPath(uuid string) string {
	return filepath.Join("/tmp/openartifact/docker-uploads", uuid)
}

func TestHandleUploadChunk_StreamsAndAccumulatesOffset(t *testing.T) {
	if err := os.MkdirAll("/tmp/openartifact/docker-uploads", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	uid := uuid.New().String()
	defer func() { _ = os.Remove(tempUploadPath(uid)) }()

	db := &fakeDB{
		repo:   &models.Repository{ID: "repo-1", Format: "docker"},
		upload: &models.DockerUpload{RepositoryID: "repo-1", UploadUUID: uid, Offset: 0},
	}
	h, _ := newTestHandler(t, db)

	chunks := []string{"hello ", "streaming ", "world"}

	var assembled strings.Builder

	for i, chunk := range chunks {
		assembled.WriteString(chunk)

		req := withChiVars(
			httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/v2/myrepo/blobs/uploads/"+uid, strings.NewReader(chunk)),
			map[string]string{"name": "myrepo", "uuid": uid},
		)
		rec := httptest.NewRecorder()

		h.handleUploadChunk(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("chunk %d: status = %d, want 202; body=%s", i, rec.Code, rec.Body.String())
		}

		wantOffset := int64(assembled.Len())
		if db.offset != wantOffset {
			t.Fatalf("chunk %d: offset = %d, want %d", i, db.offset, wantOffset)
		}

		// Range header reflects the new high-water mark.
		if got, want := rec.Header().Get("Range"), "0-"; !strings.HasPrefix(got, want) {
			t.Fatalf("chunk %d: Range header = %q", i, got)
		}
	}

	// The temp file must contain exactly the streamed bytes, in order.
	data, err := os.ReadFile(tempUploadPath(uid))
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}

	if string(data) != assembled.String() {
		t.Fatalf("temp file = %q, want %q", string(data), assembled.String())
	}
}

func TestHandleCompleteBlobUpload_MonolithicBodyAndDigest(t *testing.T) {
	if err := os.MkdirAll("/tmp/openartifact/docker-uploads", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	payload := []byte("the entire blob arrives in the final PUT")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	t.Run("correct digest stores blob", func(t *testing.T) {
		uid := uuid.New().String()
		defer func() { _ = os.Remove(tempUploadPath(uid)) }()

		db := &fakeDB{
			repo:   &models.Repository{ID: "repo-1", Format: "docker"},
			upload: &models.DockerUpload{RepositoryID: "repo-1", UploadUUID: uid, Offset: 0},
		}
		h, store := newTestHandler(t, db)

		req := withChiVars(
			httptest.NewRequestWithContext(context.Background(), http.MethodPut,
				"/v2/myrepo/blobs/uploads/"+uid+"?digest="+digest,
				strings.NewReader(string(payload))),
			map[string]string{"name": "myrepo", "uuid": uid},
		)
		// httptest.NewRequest does not populate RawQuery parsing for Query(); set it.
		q := req.URL.Query()
		q.Set("digest", digest)
		req.URL.RawQuery = q.Encode()

		rec := httptest.NewRecorder()
		h.handleCompleteBlobUpload(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
		}

		if db.createdBlob == nil {
			t.Fatal("expected CreateDockerBlob to be called")
		}

		if db.createdBlob.Size != int64(len(payload)) {
			t.Fatalf("blob size = %d, want %d", db.createdBlob.Size, len(payload))
		}

		if db.createdBlob.Digest != digest {
			t.Fatalf("blob digest = %q, want %q", db.createdBlob.Digest, digest)
		}

		if !db.deletedUpload {
			t.Fatal("expected upload record to be deleted after completion")
		}

		if rec.Header().Get("Docker-Content-Digest") != digest {
			t.Fatalf("Docker-Content-Digest = %q, want %q", rec.Header().Get("Docker-Content-Digest"), digest)
		}

		// Read the stored blob back: guards the Seek(0)+reuse path. A missing
		// rewind would store an empty (EOF) blob and fail this comparison.
		rc, err := store.Get(context.Background(), db.createdBlob.BlobSHA256)
		if err != nil {
			t.Fatalf("store.Get: %v", err)
		}
		defer func() { _ = rc.Close() }()

		stored, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read stored blob: %v", err)
		}

		if string(stored) != string(payload) {
			t.Fatalf("stored blob = %q, want %q", string(stored), string(payload))
		}
	})

	t.Run("digest mismatch is rejected", func(t *testing.T) {
		uid := uuid.New().String()
		defer func() { _ = os.Remove(tempUploadPath(uid)) }()

		db := &fakeDB{
			repo:   &models.Repository{ID: "repo-1", Format: "docker"},
			upload: &models.DockerUpload{RepositoryID: "repo-1", UploadUUID: uid, Offset: 0},
		}
		h, _ := newTestHandler(t, db)

		wrong := "sha256:" + strings.Repeat("0", 64)
		req := withChiVars(
			httptest.NewRequestWithContext(context.Background(), http.MethodPut,
				"/v2/myrepo/blobs/uploads/"+uid, strings.NewReader(string(payload))),
			map[string]string{"name": "myrepo", "uuid": uid},
		)
		q := req.URL.Query()
		q.Set("digest", wrong)
		req.URL.RawQuery = q.Encode()

		rec := httptest.NewRecorder()
		h.handleCompleteBlobUpload(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}

		if db.createdBlob != nil {
			t.Fatal("blob must not be stored on digest mismatch")
		}
	})
}
