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
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeDB satisfies the handler store by embedding the interface; only the
// handful of methods used by the blob-upload paths are implemented. Any other
// call panics, which keeps the fake honest about what the handlers touch.
type fakeDB struct {
	testHandlerStore
	uploadLeases

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

func (f *fakeDB) GetDockerUpload(_ context.Context, repositoryID, image, uploadUUID string) (*models.DockerUpload, error) {
	if !f.uploadMatches(repositoryID, uploadUUID) || f.upload.Image != image {
		return nil, nil
	}

	return f.upload, nil
}

func (f *fakeDB) uploadKey(repositoryID, uploadUUID string) string {
	return repositoryID + "|" + uploadUUID
}

func (f *fakeDB) AcquireDockerUploadLease(
	_ context.Context, repositoryID, image, uploadUUID, token string, ttl time.Duration,
) (*models.DockerUpload, error) {
	if !f.uploadMatches(repositoryID, uploadUUID) || f.upload.Image != image ||
		!f.acquire(f.uploadKey(repositoryID, uploadUUID), token, ttl) {
		return nil, nil
	}

	row := *f.upload

	return &row, nil
}

func (f *fakeDB) RenewDockerUploadLease(_ context.Context, repositoryID, uploadUUID, token string, ttl time.Duration) (bool, error) {
	return f.renew(f.uploadKey(repositoryID, uploadUUID), token, ttl), nil
}

func (f *fakeDB) ReleaseDockerUploadLease(_ context.Context, repositoryID, uploadUUID, token string) error {
	f.release(f.uploadKey(repositoryID, uploadUUID), token)

	return nil
}

func (f *fakeDB) AdvanceDockerUpload(
	_ context.Context, repositoryID, uploadUUID, token string,
	oldOffset, newOffset int64, hashState []byte, stageState string, ttl time.Duration, keepLease bool,
) (bool, error) {
	if !f.uploadMatches(repositoryID, uploadUUID) {
		return false, nil
	}

	if !f.advance(f.uploadKey(repositoryID, uploadUUID), f.upload, token, oldOffset, newOffset, hashState, stageState, ttl, keepLease) {
		return false, nil
	}

	f.offset = newOffset

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
	f.drop(f.uploadKey(repositoryID, uploadUUID))

	return nil
}

// testHandlerStore is the part of the database newTestHandler needs.
type testHandlerStore interface {
	HTTPStore
}

func newTestHandler(t *testing.T, db testHandlerStore) (*HTTPHandler, storage.BlobStore) {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	h := NewHTTPHandler(log, db, store)
	return h, store
}

// withChiVars attaches chi URL params so urlparams.Vars resolves them.
func withChiVars(r *http.Request, kv map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range kv {
		rctx.URLParams.Add(k, v)
	}

	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestHandleUploadChunk_StreamsAndAccumulatesOffset(t *testing.T) {
	uid := uuid.New().String()

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
			httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPatch,
				"/v2/myrepo/blobs/uploads/"+uid,
				strings.NewReader(chunk),
			),
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

	// The session holds one upload object with exactly the streamed bytes, and
	// the digest state it recorded is the sha256 of them.
	ids := uploadObjects(t, h)
	if len(ids) != 1 || ids[0] != uid {
		t.Fatalf("upload objects = %v, want the one of session %s", ids, uid)
	}

	object, err := h.uploads.CompleteUpload(
		context.Background(),
		uid,
		storage.UploadPosition{State: db.upload.StageState, Offset: db.upload.Offset},
	)
	if err != nil {
		t.Fatalf("complete upload: %v", err)
	}

	rc, err := h.uploads.OpenUpload(context.Background(), uid, object.Version)
	if err != nil {
		t.Fatalf("open upload: %v", err)
	}

	data, err := io.ReadAll(rc)
	_ = rc.Close()

	if err != nil || string(data) != assembled.String() {
		t.Fatalf("upload object = %q (%v), want %q", data, err, assembled.String())
	}

	session := &uploadSession{upload: db.upload}

	digest, err := session.resumeDigest()
	if err != nil {
		t.Fatalf("resume digest: %v", err)
	}

	if got, want := hex.EncodeToString(digest.Sum(nil)), sha256Hex(assembled.String()); got != want {
		t.Fatalf("recorded digest state = %s, want sha256 of the streamed bytes %s", got, want)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

func TestHandleCompleteBlobUpload_MonolithicBodyAndDigest(t *testing.T) {
	payload := []byte("the entire blob arrives in the final PUT")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	t.Run("correct digest stores blob", func(t *testing.T) {
		uid := uuid.New().String()

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
