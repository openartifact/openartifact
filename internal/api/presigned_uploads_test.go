// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// stagingBlobStore is a BlobStore with an in-memory staging area.
type stagingBlobStore struct {
	storage.BlobStore

	staged   map[string][]byte
	promoted map[string]string // sha256 -> staging id it was copied from
	deleted  []string
}

func (s *stagingBlobStore) PresignStagingPut(_ context.Context, id string, size int64, ttl time.Duration) (storage.PresignedPut, error) {
	return storage.PresignedPut{
		URL:     fmt.Sprintf("https://s3.example/artifacts/staging/%s?X-Amz-Signature=sig&size=%d", id, size),
		Headers: map[string]string{"X-Amz-Meta-Test": "1"},
		Expires: time.Now().Add(ttl),
	}, nil
}

func (s *stagingBlobStore) StatStaging(_ context.Context, id string) (int64, error) {
	b, ok := s.staged[id]
	if !ok {
		return 0, storage.ErrStagingNotFound
	}

	return int64(len(b)), nil
}

func (s *stagingBlobStore) OpenStaging(_ context.Context, id string) (io.ReadCloser, error) {
	b, ok := s.staged[id]
	if !ok {
		return nil, storage.ErrStagingNotFound
	}

	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *stagingBlobStore) PromoteStaging(_ context.Context, id, sha string, _ map[string]string) error {
	if _, ok := s.staged[id]; !ok {
		return storage.ErrStagingNotFound
	}

	s.promoted[sha] = id

	return nil
}

func (s *stagingBlobStore) DeleteStaging(_ context.Context, id string) error {
	delete(s.staged, id)
	s.deleted = append(s.deleted, id)

	return nil
}

type presignUploadDB struct {
	database.BaseMockDB

	repo      *models.Repository
	uploads   map[string]*models.PresignedUpload
	artifacts map[string]*models.RawArtifact
}

func (d *presignUploadDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if d.repo != nil && d.repo.Name == name {
		return d.repo, nil
	}

	return nil, nil
}

func (d *presignUploadDB) CreatePresignedUpload(_ context.Context, u *models.PresignedUpload) error {
	u.ID = uuid.NewString()
	u.CreatedAt = time.Now()
	d.uploads[u.ID] = u

	return nil
}

func (d *presignUploadDB) GetPresignedUpload(_ context.Context, repoID, id string) (*models.PresignedUpload, error) {
	if u := d.uploads[id]; u != nil && u.RepositoryID == repoID {
		return u, nil
	}

	return nil, nil
}

func (d *presignUploadDB) DeletePresignedUpload(_ context.Context, id string) error {
	delete(d.uploads, id)
	return nil
}

func (d *presignUploadDB) GetRawArtifact(_ context.Context, _, path string) (*models.RawArtifact, error) {
	return d.artifacts[path], nil
}

func (d *presignUploadDB) UpsertRawArtifact(_ context.Context, a *models.RawArtifact) error {
	d.artifacts[a.Path] = a
	return nil
}

type presignUploadFixture struct {
	router chi.Router
	db     *presignUploadDB
	store  *stagingBlobStore
}

func newPresignUploadFixture(t *testing.T, format, repoType string) *presignUploadFixture {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("filesystem store: %v", err)
	}

	f := &presignUploadFixture{
		db: &presignUploadDB{
			repo:      &models.Repository{ID: "repo-1", Name: "files", Format: format, RepoType: repoType},
			uploads:   map[string]*models.PresignedUpload{},
			artifacts: map[string]*models.RawArtifact{},
		},
		store:  &stagingBlobStore{BlobStore: local, staged: map[string][]byte{}, promoted: map[string]string{}},
		router: chi.NewRouter(),
	}

	NewPresignedUploadHandler(log, f.db, f.store, auth.NewEvaluator(f.db, log, false), 30*time.Minute, nil).Register(f.router)

	return f
}

func (f *presignUploadFixture) call(t *testing.T, anonymous bool, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, rd)

	p := &auth.Principal{Username: "admin", IsLegacy: true}
	if anonymous {
		p = &auth.Principal{IsAnonymous: true}
	}

	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)

	return rw
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (f *presignUploadFixture) start(t *testing.T, content []byte) CreatePresignedUploadResponse {
	t.Helper()

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{
		Path: "releases/app.bin", Size: int64(len(content)), SHA256: digestOf(content), ContentType: "application/x-test",
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", rw.Code, rw.Body.String())
	}

	var resp CreatePresignedUploadResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	return resp
}

func completePath(id string) string { return "/api/v1/repositories/files/uploads/" + id + "/complete" }

func TestPresignedUpload_CreateReturnsSignedPut(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	content := []byte("hello presigned world")

	resp := f.start(t, content)

	if resp.Method != http.MethodPut || !strings.Contains(resp.UploadURL, "/staging/"+resp.ID) || resp.Headers["X-Amz-Meta-Test"] != "1" {
		t.Errorf("unexpected response %+v", resp)
	}

	row := f.db.uploads[resp.ID]
	if row == nil || row.Path != "releases/app.bin" || row.SizeBytes != int64(len(content)) || row.SHA256 != digestOf(content) || row.CreatedBy != "admin" {
		t.Fatalf("recorded upload = %+v", row)
	}

	if earliest := time.Now().Add(30*time.Minute + 50*time.Minute); row.ExpiresAt.Before(earliest) {
		t.Errorf("ExpiresAt %v leaves no grace after the URL TTL", row.ExpiresAt)
	}
}

func TestPresignedUpload_CreateValidation(t *testing.T) {
	good := digestOf([]byte("x"))

	tests := []struct {
		name     string
		format   string
		repoType string
		anon     bool
		body     CreatePresignedUploadRequest
		want     int
	}{
		{"not raw", "maven", "hosted", false, CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: good}, http.StatusBadRequest},
		{"proxy repo", "raw", "proxy", false, CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: good}, http.StatusBadRequest},
		{"anonymous", "raw", "hosted", true, CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: good}, http.StatusUnauthorized},
		{"empty path", "raw", "hosted", false, CreatePresignedUploadRequest{Size: 1, SHA256: good}, http.StatusBadRequest},
		{"dotdot path", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a/../b", Size: 1, SHA256: good}, http.StatusBadRequest},
		{"directory path", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a/", Size: 1, SHA256: good}, http.StatusBadRequest},
		{"short sha", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: "abc"}, http.StatusBadRequest},
		{"non-hex sha", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: strings.Repeat("z", 64)}, http.StatusBadRequest},
		{"negative size", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a", Size: -1, SHA256: good}, http.StatusBadRequest},
		{"too large", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a", Size: 6 << 30, SHA256: good}, http.StatusBadRequest},
		{"upper-case sha is normalised", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: strings.ToUpper(good)}, http.StatusCreated},
		{"zero byte file", "raw", "hosted", false, CreatePresignedUploadRequest{Path: "a", Size: 0, SHA256: digestOf(nil)}, http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPresignUploadFixture(t, tt.format, tt.repoType)

			rw := f.call(t, tt.anon, "/api/v1/repositories/files/uploads", tt.body)
			if rw.Code != tt.want {
				t.Fatalf("status = %d, want %d; body %s", rw.Code, tt.want, rw.Body.String())
			}

			if tt.want != http.StatusCreated && len(f.db.uploads) != 0 {
				t.Errorf("a rejected request left %d upload rows", len(f.db.uploads))
			}
		})
	}
}

func TestPresignedUpload_UnsupportedBackend(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	log, _ := logger.New("error", "json", "stderr")

	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	NewPresignedUploadHandler(log, f.db, local, auth.NewEvaluator(f.db, log, false), time.Minute, nil).Register(r)
	f.router = r

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: digestOf([]byte("x"))})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rw.Code)
	}
}

func TestPresignedUpload_CompleteRecordsArtifact(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	content := []byte("hello presigned world")
	resp := f.start(t, content)

	f.store.staged[resp.ID] = content

	rw := f.call(t, false, completePath(resp.ID), nil)
	if rw.Code != http.StatusCreated {
		t.Fatalf("complete status = %d, body %s", rw.Code, rw.Body.String())
	}

	var out CompletePresignedUploadResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if out.Path != "releases/app.bin" || out.SHA256 != digestOf(content) || out.Size != int64(len(content)) || out.ID == "" {
		t.Errorf("response = %+v", out)
	}

	art := f.db.artifacts["releases/app.bin"]
	if art == nil || art.BlobKey != digestOf(content) || art.SHA256 != digestOf(content) || art.SizeBytes != int64(len(content)) || art.ContentType != "application/x-test" {
		t.Fatalf("recorded artifact = %+v", art)
	}

	if f.store.promoted[digestOf(content)] != resp.ID {
		t.Error("staging object was not promoted to its content-addressed key")
	}

	if _, still := f.store.staged[resp.ID]; still {
		t.Error("staging object not deleted after completion")
	}

	if len(f.db.uploads) != 0 {
		t.Error("upload row not deleted after completion")
	}

	// A second complete finds nothing: the upload is spent.
	if again := f.call(t, false, completePath(resp.ID), nil); again.Code != http.StatusNotFound {
		t.Errorf("repeated complete status = %d, want 404", again.Code)
	}
}

func TestPresignedUpload_CompleteDetectsContentType(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	content := []byte("<!DOCTYPE html><html><body>x</body></html>")

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{Path: "x.html", Size: int64(len(content)), SHA256: digestOf(content)})

	var resp CreatePresignedUploadResponse
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)

	f.store.staged[resp.ID] = content

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}

	if ct := f.db.artifacts["x.html"].ContentType; !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q, want sniffed text/html", ct)
	}
}

func TestPresignedUpload_CompleteRejectsMismatch(t *testing.T) {
	declared := []byte("the declared content")

	for name, uploaded := range map[string][]byte{
		"sha256 mismatch, same size": []byte("THE DECLARED CONTENT"),
		"size mismatch (shorter)":    []byte("short"),
		"size mismatch (longer)":     append(append([]byte{}, declared...), "extra"...),
	} {
		t.Run(name, func(t *testing.T) {
			f := newPresignUploadFixture(t, "raw", "hosted")
			resp := f.start(t, declared)

			f.store.staged[resp.ID] = uploaded

			rw := f.call(t, false, completePath(resp.ID), nil)
			if rw.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body %s", rw.Code, rw.Body.String())
			}

			if len(f.db.artifacts) != 0 {
				t.Errorf("a rejected upload recorded %d artifacts", len(f.db.artifacts))
			}

			if len(f.store.promoted) != 0 {
				t.Error("a rejected upload was promoted to a content-addressed blob")
			}

			if _, still := f.store.staged[resp.ID]; still {
				t.Error("staging object of a rejected upload was not deleted")
			}

			if len(f.db.uploads) != 0 {
				t.Error("upload row of a rejected upload was not deleted")
			}
		})
	}
}

func TestPresignedUpload_CompleteBeforeUploadKeepsRow(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	resp := f.start(t, []byte("data"))

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rw.Code)
	}

	if f.db.uploads[resp.ID] == nil {
		t.Error("the upload was dropped; the client can still upload and complete")
	}
}

func TestPresignedUpload_CompleteExpiredOrUnknown(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	resp := f.start(t, []byte("data"))

	f.db.uploads[resp.ID].ExpiresAt = time.Now().Add(-time.Minute)
	f.store.staged[resp.ID] = []byte("data")

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusGone {
		t.Fatalf("expired status = %d, want 410", rw.Code)
	}

	if len(f.db.artifacts) != 0 || len(f.db.uploads) != 0 {
		t.Error("expired upload was recorded or kept")
	}

	if rw := f.call(t, false, completePath(uuid.NewString()), nil); rw.Code != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", rw.Code)
	}

	if rw := f.call(t, false, completePath("not-a-uuid"), nil); rw.Code != http.StatusNotFound {
		t.Errorf("malformed id status = %d, want 404", rw.Code)
	}
}

func TestPresignedUpload_CompleteOverwriteKeepsID(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	f.db.artifacts["releases/app.bin"] = &models.RawArtifact{ID: "existing-id", Path: "releases/app.bin", SHA256: digestOf([]byte("old")), BlobKey: digestOf([]byte("old"))}

	content := []byte("new content")
	resp := f.start(t, content)
	f.store.staged[resp.ID] = content

	rw := f.call(t, false, completePath(resp.ID), nil)
	if rw.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}

	var out CompletePresignedUploadResponse
	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	if out.ID != "existing-id" {
		t.Errorf("id = %q, want the overwritten row's id like a raw PUT", out.ID)
	}
}
