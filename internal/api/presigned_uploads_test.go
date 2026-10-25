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
	"errors"
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
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// stagingBlobStore is a BlobStore with an in-memory staging area that, like
// S3, versions objects (the version is the content hash, as an ETag is) and
// honours the If-Match preconditions of OpenStaging and PromoteStaging.
type stagingBlobStore struct {
	storage.BlobStore

	staged   map[string][]byte
	promoted map[string]string // sha256 -> staging id it was copied from
	copied   map[string][]byte // sha256 -> the bytes that were copied
	deleted  []string

	// afterStat and afterOpen run at the points where a client holding the
	// still-valid presigned URL can overwrite the staging object.
	afterStat func()
	afterOpen func()
}

func versionOf(b []byte) string { return `"` + digestOf(b) + `"` }

func (s *stagingBlobStore) StagingSupported() bool { return true }

func (s *stagingBlobStore) PresignStagingPut(_ context.Context, id string, size int64, ttl time.Duration) (storage.PresignedPut, error) {
	return storage.PresignedPut{
		URL:     fmt.Sprintf("https://s3.example/artifacts/staging/%s?X-Amz-Signature=sig&size=%d", id, size),
		Headers: map[string]string{"X-Amz-Meta-Test": "1"},
		Expires: time.Now().Add(ttl),
	}, nil
}

func (s *stagingBlobStore) StatStaging(_ context.Context, id string) (storage.StagedObject, error) {
	b, ok := s.staged[id]
	if !ok {
		return storage.StagedObject{}, storage.ErrStagingNotFound
	}

	obj := storage.StagedObject{Size: int64(len(b)), Version: versionOf(b)}

	if s.afterStat != nil {
		s.afterStat()
	}

	return obj, nil
}

func (s *stagingBlobStore) OpenStaging(_ context.Context, id, version string) (io.ReadCloser, error) {
	b, ok := s.staged[id]
	if !ok {
		return nil, storage.ErrStagingNotFound
	}

	if versionOf(b) != version {
		return nil, storage.ErrStagingChanged
	}

	snapshot := bytes.Clone(b)

	if s.afterOpen != nil {
		s.afterOpen()
	}

	return io.NopCloser(bytes.NewReader(snapshot)), nil
}

func (s *stagingBlobStore) PromoteStaging(_ context.Context, id, sha, version string, _ map[string]string) error {
	b, ok := s.staged[id]
	if !ok {
		return storage.ErrStagingNotFound
	}

	if versionOf(b) != version {
		return storage.ErrStagingChanged
	}

	s.promoted[sha] = id
	s.copied[sha] = bytes.Clone(b)

	return nil
}

func (s *stagingBlobStore) DeleteStaging(_ context.Context, id string) error {
	delete(s.staged, id)
	s.deleted = append(s.deleted, id)

	return nil
}

func (s *stagingBlobStore) WalkStaging(context.Context, time.Time, func([]string) error) error {
	return nil
}

type presignUploadDB struct {
	databasetest.BaseMockDB

	repo      *models.Repository
	uploads   map[string]*models.PresignedUpload
	artifacts map[string]*models.RawArtifact
	targets   map[string][]*models.PermissionTarget // by user id
	claims    int

	precheck       *models.StorageQuota // served to the advisory quota precheck when set
	quotaLimit     int64                // 0 = unlimited
	quotaUsed      int64
	quotaArtifacts int
}

func (d *presignUploadDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	if d.quotaLimit > 0 && d.quotaUsed+size > d.quotaLimit {
		return false, nil
	}

	d.quotaUsed += size
	d.quotaArtifacts += count

	return true, nil
}

func (d *presignUploadDB) GetOrCreateStorageQuota(context.Context, string) (*models.StorageQuota, error) {
	if d.precheck == nil {
		return nil, errPresignNoQuotaRow
	}

	return d.precheck, nil
}

func (d *presignUploadDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.quotaUsed -= size
	d.quotaArtifacts -= count

	return nil
}

func (d *presignUploadDB) GetPermissionTargetsForPrincipal(_ context.Context, userID string, _ []string) ([]*models.PermissionTarget, error) {
	return d.targets[userID], nil
}

func (d *presignUploadDB) ClaimPresignedUpload(_ context.Context, repoID, id string, lease time.Duration) (bool, error) {
	u := d.uploads[id]
	if u == nil || u.RepositoryID != repoID || u.CompletedAt != nil {
		return false, nil
	}

	if u.CompletingSince != nil && time.Since(*u.CompletingSince) < lease {
		return false, nil
	}

	now := time.Now()
	u.CompletingSince = &now
	d.claims++

	return true, nil
}

func (d *presignUploadDB) ReleasePresignedUpload(_ context.Context, id string) error {
	if u := d.uploads[id]; u != nil {
		u.CompletingSince = nil
	}

	return nil
}

func (d *presignUploadDB) MarkPresignedUploadCompleted(_ context.Context, id, artifactID string) error {
	if u := d.uploads[id]; u != nil {
		now := time.Now()
		u.CompletedAt, u.CompletingSince, u.ArtifactID = &now, nil, artifactID
	}

	return nil
}

func (d *presignUploadDB) CreatePresignedUploadCapped(ctx context.Context, u *models.PresignedUpload, maxPending int) error {
	if maxPending > 0 {
		n := 0

		for _, o := range d.uploads {
			if o.CreatedBy == u.CreatedBy && o.CompletedAt == nil && o.ExpiresAt.After(time.Now()) {
				n++
			}
		}

		if n >= maxPending {
			return database.ErrPresignedUploadCap
		}
	}

	return d.CreatePresignedUpload(ctx, u)
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
	router  chi.Router
	handler *PresignedUploadHandler
	db      *presignUploadDB
	store   *stagingBlobStore
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
			targets: map[string][]*models.PermissionTarget{
				"writer-1": {{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: []string{"read", "write"}}},
				"other-1":  {{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: []string{"read", "write"}}},
				"admin-1":  {{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: []string{"manage", "read", "write"}}},
			},
		},
		store:  &stagingBlobStore{BlobStore: local, staged: map[string][]byte{}, promoted: map[string]string{}, copied: map[string][]byte{}},
		router: chi.NewRouter(),
	}

	f.handler = NewPresignedUploadHandler(log, f.db, f.store, auth.NewEvaluator(f.db, log, false), 30*time.Minute, 2, nil)
	f.handler.Register(f.router)

	return f
}

func (f *presignUploadFixture) call(t *testing.T, anonymous bool, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	p := &auth.Principal{Username: "admin", IsLegacy: true}
	if anonymous {
		p = &auth.Principal{IsAnonymous: true}
	}

	return f.callAs(t, p, path, body)
}

func userPrincipal(name, id string) *auth.Principal {
	return &auth.Principal{Username: name, UserID: &id}
}

func (f *presignUploadFixture) callAs(t *testing.T, p *auth.Principal, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, rd)

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

var errPresignNoQuotaRow = errors.New("no quota row")

// A declared size the repository quota cannot take is refused at create time,
// before any bytes are parked in the bucket.
func TestPresignedUpload_CreateRefusesOverQuotaSize(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	f.db.precheck = &models.StorageQuota{MaxSizeBytes: 10, CurrentSizeBytes: 4}

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{
		Path: "releases/app.bin", Size: 7, SHA256: digestOf([]byte("x")),
	})
	if rw.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body %s", rw.Code, rw.Body.String())
	}

	if len(f.db.uploads) != 0 {
		t.Errorf("upload rows = %d, want none", len(f.db.uploads))
	}
}

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
	NewPresignedUploadHandler(log, f.db, local, auth.NewEvaluator(f.db, log, false), time.Minute, 0, nil).Register(r)
	f.router = r

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: digestOf([]byte("x"))})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rw.Code)
	}

	if len(f.db.uploads) != 0 {
		t.Errorf("an unsupported backend left %d upload rows", len(f.db.uploads))
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

	if row := f.db.uploads[resp.ID]; row == nil || row.CompletedAt == nil || row.ArtifactID != out.ID {
		t.Errorf("upload row = %+v, want it kept and marked completed for retries", row)
	}

	// A retried complete (lost response) returns the original result and
	// does not record the artifact a second time.
	again := f.call(t, false, completePath(resp.ID), nil)
	if again.Code != http.StatusOK || again.Body.String() != rw.Body.String() {
		t.Errorf("repeated complete = %d %s, want 200 with the original body %s", again.Code, again.Body.String(), rw.Body.String())
	}

	if f.db.claims != 1 {
		t.Errorf("complete claimed the upload %d times, want 1", f.db.claims)
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

// STOR-7: an expired upload whose complete is still verifying (lease held) must
// not have its staging object deleted from under that call.
func TestPresignedUpload_ExpiredCompleteRespectsActiveLease(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	resp := f.start(t, []byte("data"))

	up := f.db.uploads[resp.ID]
	up.ExpiresAt = time.Now().Add(-time.Minute)
	lease := time.Now()
	up.CompletingSince = &lease
	f.store.staged[resp.ID] = []byte("data")

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusConflict {
		t.Fatalf("expired complete during an active lease = %d, want 409", rw.Code)
	}

	if f.db.uploads[resp.ID] == nil || f.store.staged[resp.ID] == nil {
		t.Error("the row or staging object was deleted while another complete holds the lease")
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

func TestPresignedUpload_CompleteReservesQuota(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	content := []byte("hello presigned world")
	resp := f.start(t, content)
	f.store.staged[resp.ID] = content

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}

	if f.db.quotaUsed != int64(len(content)) || f.db.quotaArtifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", f.db.quotaUsed, f.db.quotaArtifacts, len(content))
	}
}

// An upload that does not fit is answered 507 and records nothing; it stays
// pending, so completing it again once space is available succeeds.
func TestPresignedUpload_CompleteOverQuotaIsRetryable(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	f.db.quotaLimit = 3

	content := []byte("hello presigned world")
	resp := f.start(t, content)
	f.store.staged[resp.ID] = content

	rw := f.call(t, false, completePath(resp.ID), nil)
	if rw.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507, body %s", rw.Code, rw.Body.String())
	}

	var out ErrorResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil || out.Code != "quota_exceeded" {
		t.Errorf("body %q, want code quota_exceeded", rw.Body.String())
	}

	if len(f.db.artifacts) != 0 || f.db.quotaUsed != 0 || f.db.quotaArtifacts != 0 {
		t.Errorf("artifacts=%d used=%d count=%d; want nothing recorded", len(f.db.artifacts), f.db.quotaUsed, f.db.quotaArtifacts)
	}

	if f.db.uploads[resp.ID] == nil || f.store.staged[resp.ID] == nil {
		t.Fatal("the pending upload or its staging object was discarded")
	}

	f.db.quotaLimit = 0

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, body %s", rw.Code, rw.Body.String())
	}

	if f.db.quotaUsed != int64(len(content)) || f.db.quotaArtifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after retry, want %d/1", f.db.quotaUsed, f.db.quotaArtifacts, len(content))
	}
}

func TestPresignedUpload_CompleteOverwriteReleasesPreviousQuota(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	f.db.artifacts["releases/app.bin"] = &models.RawArtifact{ID: "existing-id", Path: "releases/app.bin", SHA256: digestOf([]byte("old")), BlobKey: digestOf([]byte("old")), SizeBytes: 3}
	f.db.quotaUsed, f.db.quotaArtifacts = 3, 1

	content := []byte("new content")
	resp := f.start(t, content)
	f.store.staged[resp.ID] = content

	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}

	if f.db.quotaUsed != int64(len(content)) || f.db.quotaArtifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", f.db.quotaUsed, f.db.quotaArtifacts, len(content))
	}
}

func TestPresignedUpload_CreateIsNotCacheable(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", CreatePresignedUploadRequest{Path: "a", Size: 1, SHA256: digestOf([]byte("x"))})
	if rw.Code != http.StatusCreated || rw.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q; the response carries a bearer URL", rw.Code, rw.Header().Get("Cache-Control"))
	}
}

// A client keeps a working PUT URL after complete starts. Whatever it writes
// after the bytes were verified must never be promoted to the content-addressed
// blob of the declared hash.
func TestPresignedUpload_ObjectSwappedAfterVerificationIsNotPromoted(t *testing.T) {
	declared := []byte("the declared content")
	evil := []byte("EVIL BYTES, same hash? no")

	for name, hook := range map[string]func(f *presignUploadFixture, id string){
		"between stat and read": func(f *presignUploadFixture, id string) {
			f.store.afterStat = func() { f.store.staged[id] = evil }
		},
		"between verification and promotion": func(f *presignUploadFixture, id string) {
			f.store.afterOpen = func() { f.store.staged[id] = evil }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPresignUploadFixture(t, "raw", "hosted")
			resp := f.start(t, declared)
			f.store.staged[resp.ID] = declared

			hook(f, resp.ID)

			rw := f.call(t, false, completePath(resp.ID), nil)
			if rw.Code != http.StatusConflict || !strings.Contains(rw.Body.String(), "upload_changed") {
				t.Fatalf("status = %d, body %s; want 409 upload_changed", rw.Code, rw.Body.String())
			}

			if len(f.store.promoted) != 0 || len(f.store.copied) != 0 {
				t.Fatalf("swapped bytes were promoted: %v", f.store.promoted)
			}

			if len(f.db.artifacts) != 0 {
				t.Errorf("artifact recorded for a swapped upload: %v", f.db.artifacts)
			}

			if _, still := f.store.staged[resp.ID]; still || f.db.uploads[resp.ID] != nil {
				t.Error("a swapped upload must be cleaned up")
			}
		})
	}
}

func TestPresignedUpload_ConcurrentCompleteIsInProgress(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	content := []byte("data")
	resp := f.start(t, content)
	f.store.staged[resp.ID] = content

	// While the first complete verifies (inside OpenStaging), a second one arrives.
	var second *httptest.ResponseRecorder

	f.store.afterOpen = func() { second = f.call(t, false, completePath(resp.ID), nil) }

	first := f.call(t, false, completePath(resp.ID), nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first complete = %d %s", first.Code, first.Body.String())
	}

	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "upload_in_progress") || second.Header().Get("Retry-After") == "" {
		t.Errorf("concurrent complete = %d %s (Retry-After %q), want 409 upload_in_progress", second.Code, second.Body.String(), second.Header().Get("Retry-After"))
	}

	if len(f.store.promoted) != 1 || len(f.db.artifacts) != 1 {
		t.Errorf("promoted %d times, recorded %d artifacts; want exactly one of each", len(f.store.promoted), len(f.db.artifacts))
	}
}

func TestPresignedUpload_CompleteSurvivesClientDisconnect(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	content := []byte("data")
	resp := f.start(t, content)
	f.store.staged[resp.ID] = content

	// The client gives up (cancelled request context) before the server finishes.
	ctx, cancel := context.WithCancel(context.Background())
	f.store.afterOpen = cancel

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, completePath(resp.ID), nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))
	f.router.ServeHTTP(httptest.NewRecorder(), req)

	if len(f.db.artifacts) != 1 || f.db.uploads[resp.ID].CompletedAt == nil {
		t.Fatalf("a cancelled request aborted the commit: artifacts %d", len(f.db.artifacts))
	}

	// The retry gets the result.
	if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusOK {
		t.Errorf("retry = %d %s, want 200", rw.Code, rw.Body.String())
	}
}

func TestPresignedUpload_CompleteBoundToCreator(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted")
	content := []byte("data")

	rw := f.callAs(t, userPrincipal("writer", "writer-1"), "/api/v1/repositories/files/uploads",
		CreatePresignedUploadRequest{Path: "a.bin", Size: int64(len(content)), SHA256: digestOf(content)})
	if rw.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rw.Code, rw.Body.String())
	}

	var resp CreatePresignedUploadResponse
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	f.store.staged[resp.ID] = content

	if rw := f.callAs(t, userPrincipal("other", "other-1"), completePath(resp.ID), nil); rw.Code != http.StatusForbidden {
		t.Fatalf("another writer completed the upload: %d %s", rw.Code, rw.Body.String())
	}

	if len(f.db.artifacts) != 0 || f.db.claims != 0 {
		t.Error("a forbidden complete must not claim or record anything")
	}

	if rw := f.callAs(t, userPrincipal("admin2", "admin-1"), completePath(resp.ID), nil); rw.Code != http.StatusCreated {
		t.Fatalf("an administrator could not complete: %d %s", rw.Code, rw.Body.String())
	}

	// The creator gets the replay.
	if rw := f.callAs(t, userPrincipal("writer", "writer-1"), completePath(resp.ID), nil); rw.Code != http.StatusOK {
		t.Errorf("creator replay = %d", rw.Code)
	}
}

func TestPresignedUpload_PendingCap(t *testing.T) {
	f := newPresignUploadFixture(t, "raw", "hosted") // cap: 2

	body := func(p string) CreatePresignedUploadRequest {
		return CreatePresignedUploadRequest{Path: p, Size: 1, SHA256: digestOf([]byte("x"))}
	}

	var ids []string

	for _, p := range []string{"a", "b"} {
		rw := f.call(t, false, "/api/v1/repositories/files/uploads", body(p))
		if rw.Code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", p, rw.Code, rw.Body.String())
		}

		var resp CreatePresignedUploadResponse
		_ = json.Unmarshal(rw.Body.Bytes(), &resp)
		ids = append(ids, resp.ID)
	}

	rw := f.call(t, false, "/api/v1/repositories/files/uploads", body("c"))
	if rw.Code != http.StatusTooManyRequests || rw.Header().Get("Retry-After") == "" {
		t.Fatalf("over the cap = %d (Retry-After %q), want 429", rw.Code, rw.Header().Get("Retry-After"))
	}

	if len(f.db.uploads) != 2 {
		t.Errorf("a refused create left %d rows", len(f.db.uploads))
	}

	// Another user is unaffected, and completing frees a slot.
	if rw := f.callAs(t, userPrincipal("writer", "writer-1"), "/api/v1/repositories/files/uploads", body("d")); rw.Code != http.StatusCreated {
		t.Errorf("another user = %d", rw.Code)
	}

	f.store.staged[ids[0]] = []byte("x")

	if rw := f.call(t, false, completePath(ids[0]), nil); rw.Code != http.StatusCreated {
		t.Fatalf("complete = %d %s", rw.Code, rw.Body.String())
	}

	if rw := f.call(t, false, "/api/v1/repositories/files/uploads", body("e")); rw.Code != http.StatusCreated {
		t.Errorf("create after a completed upload = %d, want a free slot", rw.Code)
	}
}
