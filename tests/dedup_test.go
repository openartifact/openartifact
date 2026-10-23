// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

func buildRPMUploadRequest(t *testing.T, rpmData []byte, repoName string) *http.Request {
	t.Helper()

	var body bytes.Buffer

	w := multipart.NewWriter(&body)

	fw, err := w.CreateFormFile("file", "test.rpm")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}

	if _, err := io.Copy(fw, bytes.NewReader(rpmData)); err != nil {
		t.Fatalf("Copy to form file: %v", err)
	}

	_ = w.Close()
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/repositories/"+repoName+"/packages", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())

	return req
}

func TestArtifactExists_NotFound(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()

	exists, err := db.ArtifactExists(ctx, "deadbeef", "repo-1")
	if err != nil {
		t.Fatalf("ArtifactExists: %v", err)
	}

	if exists {
		t.Error("expected false for non-existent sha256+repo")
	}
}

func TestArtifactExists_Found(t *testing.T) {
	db := newMockDatabase()
	ctx := context.Background()

	comp := &models.Component{
		ID:           "comp-1",
		RepositoryID: "repo-1",
		ArtifactID:   "blob-1",
		SHA256:       "abc123",
		Format:       "rpm",
		Name:         "testpkg",
		Version:      "1.0",
	}
	db.components["comp-1"] = comp

	exists, err := db.ArtifactExists(ctx, "abc123", "repo-1")
	if err != nil {
		t.Fatalf("ArtifactExists: %v", err)
	}

	if !exists {
		t.Error("expected true for existing sha256+repo")
	}

	exists, err = db.ArtifactExists(ctx, "abc123", "repo-2")
	if err != nil {
		t.Fatalf("ArtifactExists different repo: %v", err)
	}

	if exists {
		t.Error("expected false for same sha256 but different repo")
	}
}

func TestExistsBySHA256_MockBlobStore_NotFound(t *testing.T) {
	store := newMockBlobStore()
	ctx := context.Background()

	blobID, exists, err := store.ExistsBySHA256(ctx, "nothash")
	if err != nil {
		t.Fatalf("ExistsBySHA256: %v", err)
	}

	if exists || blobID != "" {
		t.Errorf("expected not found, got exists=%v blobID=%q", exists, blobID)
	}
}

func TestExistsBySHA256_MockBlobStore_Found(t *testing.T) {
	store := newMockBlobStore()
	ctx := context.Background()
	sha := "abc123deadbeef"
	store.blobs["blob-x"] = []byte("data")
	store.sha256Map[sha] = "blob-x"

	blobID, exists, err := store.ExistsBySHA256(ctx, sha)
	if err != nil {
		t.Fatalf("ExistsBySHA256: %v", err)
	}

	if !exists {
		t.Error("expected exists=true")
	}

	if blobID != "blob-x" {
		t.Errorf("expected blobID 'blob-x', got %q", blobID)
	}
}

func TestExistsBySHA256_FilesystemStore_NotFound(t *testing.T) {
	dir := t.TempDir()

	store, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	blobID, exists, err := store.ExistsBySHA256(context.Background(), "0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatalf("ExistsBySHA256: %v", err)
	}

	if exists || blobID != "" {
		t.Errorf("expected not found, got exists=%v blobID=%q", exists, blobID)
	}
}

func TestExistsBySHA256_FilesystemStore_Found(t *testing.T) {
	dir := t.TempDir()

	store, err := storage.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	ctx := context.Background()
	data := []byte("hello world content")

	blobID, err := store.Put(ctx, bytes.NewReader(data), map[string]string{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if blobID == "" {
		t.Fatal("expected non-empty blobID from Put")
	}

	meta, err := store.GetMetadata(ctx, blobID)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}

	actualSHA := meta["_sha256"]
	if actualSHA == "" {
		t.Fatal("expected _sha256 in metadata")
	}

	foundID, exists, err := store.ExistsBySHA256(ctx, actualSHA)
	if err != nil {
		t.Fatalf("ExistsBySHA256: %v", err)
	}

	if !exists {
		t.Error("expected exists=true after Put")
	}

	if foundID == "" {
		t.Error("expected non-empty blobID from ExistsBySHA256")
	}
}

func buildRPMRouter(t *testing.T, db *MockDatabase, blobStore *MockBlobStore, repoName string) chi.Router {
	t.Helper()
	log := newHandlerLogger(t)
	h := api.NewRPMUploadHandler(log, db, blobStore, nil)
	router := chi.NewRouter()
	h.Register(router)

	return router
}

func seedTestRepo(db *MockDatabase, id, name string) {
	db.repositories[id] = &models.Repository{
		ID:     id,
		Name:   name,
		Format: "rpm",
	}
}

func TestDedup_SamePackageTwice_SecondGets409(t *testing.T) {
	db := newMockDatabase()
	blobStore := newMockBlobStore()
	repoName := "testrepo"
	repoID := "repo-dedup-1"
	seedTestRepo(db, repoID, repoName)

	router := buildRPMRouter(t, db, blobStore, repoName)
	rpmData := buildMinimalRPM()

	req1 := buildRPMUploadRequest(t, rpmData, repoName)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("first upload: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}

	req2 := buildRPMUploadRequest(t, rpmData, repoName)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Fatalf("second upload: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal 409 response: %v", err)
	}

	if data, ok := resp["data"].(map[string]any); !ok || data["duplicate"] != true {
		t.Errorf("expected duplicate=true in response data, got %v", resp)
	}
}

func TestDedup_SamePackageDifferentRepo_Both200(t *testing.T) {
	db := newMockDatabase()
	blobStore := newMockBlobStore()

	seedTestRepo(db, "repo-a-id", "repo-a")
	seedTestRepo(db, "repo-b-id", "repo-b")

	rpmData := buildMinimalRPM()

	logA, _ := logger.New("error", "json", "stderr")
	hA := api.NewRPMUploadHandler(logA, db, blobStore, nil)
	routerA := chi.NewRouter()
	hA.Register(routerA)

	logB, _ := logger.New("error", "json", "stderr")
	hB := api.NewRPMUploadHandler(logB, db, blobStore, nil)
	routerB := chi.NewRouter()
	hB.Register(routerB)

	reqA := buildRPMUploadRequest(t, rpmData, "repo-a")
	wA := httptest.NewRecorder()
	routerA.ServeHTTP(wA, reqA)

	if wA.Code != http.StatusOK {
		t.Fatalf("repo-a upload: expected 200, got %d: %s", wA.Code, wA.Body.String())
	}

	reqB := buildRPMUploadRequest(t, rpmData, "repo-b")
	wB := httptest.NewRecorder()
	routerB.ServeHTTP(wB, reqB)

	if wB.Code != http.StatusOK {
		t.Fatalf("repo-b upload: expected 200, got %d: %s", wB.Code, wB.Body.String())
	}
}

func TestDedup_ConcurrentSamePackage_ExactlyOneSucceeds(t *testing.T) {
	db := newMockDatabase()
	blobStore := newMockBlobStore()
	repoName := "concurrent-repo"
	repoID := "repo-concurrent-1"
	seedTestRepo(db, repoID, repoName)

	log, _ := logger.New("error", "json", "stderr")
	h := api.NewRPMUploadHandler(log, db, blobStore, nil)
	router := chi.NewRouter()
	h.Register(router)

	rpmData := buildMinimalRPM()
	n := 5

	codes := make([]int, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			req := buildRPMUploadRequest(t, rpmData, repoName)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			codes[idx] = w.Code
		}(i)
	}

	wg.Wait()

	ok := 0
	conflict := 0

	for _, code := range codes {
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Errorf("unexpected status code: %d", code)
		}
	}

	if ok != 1 {
		t.Errorf("expected exactly 1 success, got %d (codes: %v)", ok, codes)
	}

	if conflict != n-1 {
		t.Errorf("expected %d conflicts, got %d (codes: %v)", n-1, conflict, codes)
	}
}

// RpmPackageExists reports whether the repository already holds a package with
// the same name/version/release/epoch/arch (the RPM immutability check).
func (m *MockDatabase) RpmPackageExists(_ context.Context, repoID, name, version, release string, epoch int, arch string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.components {
		if c.RepositoryID == repoID && c.Name == name && c.Version == version &&
			c.Release == release && c.Epoch == epoch && c.Arch == arch {
			return true, nil
		}
	}

	return false, nil
}

// BlobReferenceCount counts the stored packages referencing the blob, like the
// real database does, so SafeDeleteBlob never removes a blob a row still uses.
func (m *MockDatabase) BlobReferenceCount(_ context.Context, sha256 string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0

	for _, c := range m.components {
		if c.SHA256 == sha256 {
			n++
		}
	}

	return n, nil
}

var mockBlobLocks sync.Map //nolint:gochecknoglobals // lock table behind the MockDatabase blob lock (no field available on the shared mock)

// WithBlobLock serializes callers per blob sha256, mirroring the advisory lock
// of the real database (the lock table is process-wide; tests only share it
// across identical content, which merely serializes them).
func (m *MockDatabase) WithBlobLock(ctx context.Context, sha256 string, fn func(ctx context.Context) error) error {
	mu, _ := mockBlobLocks.LoadOrStore(sha256, &sync.Mutex{})

	lock, _ := mu.(*sync.Mutex)
	lock.Lock()

	defer lock.Unlock()

	return fn(ctx)
}

// rpmLookupDB adds the by-id package lookup the delete path needs.
type rpmLookupDB struct{ *MockDatabase }

func (d rpmLookupDB) GetRpmPackageByID(_ context.Context, id string) (*models.RpmPackage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	c := d.components[id]
	if c == nil {
		return nil, nil
	}

	return &models.RpmPackage{
		ID: c.ID, RepositoryID: c.RepositoryID, Name: c.Name, Version: c.Version,
		Release: c.Release, Filename: c.Filename, PackageSize: c.PackageSize,
	}, nil
}

// deleteRecordingStore records Delete calls (the mock store keys blobs by a
// generated id, not by sha256 like the real stores).
type deleteRecordingStore struct {
	*MockBlobStore

	dmu     sync.Mutex
	deleted []string
}

func (s *deleteRecordingStore) Delete(ctx context.Context, id string) error {
	s.dmu.Lock()
	s.deleted = append(s.deleted, id)
	s.dmu.Unlock()

	return s.MockBlobStore.Delete(ctx, id)
}

func (s *deleteRecordingStore) deletedIDs() []string {
	s.dmu.Lock()
	defer s.dmu.Unlock()

	return append([]string(nil), s.deleted...)
}

type publishedEvent struct {
	repoID, event string
	payload       map[string]any
}

type recordingPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

func (p *recordingPublisher) Dispatch(_ context.Context, repoID, eventType string, payload map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.events = append(p.events, publishedEvent{repoID, eventType, payload})
}

func (p *recordingPublisher) snapshot() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]publishedEvent(nil), p.events...)
}

func newRPMUploadRouter(t *testing.T, db database.Database, blobStore storage.BlobStore, pub *recordingPublisher) chi.Router {
	t.Helper()

	h := api.NewRPMUploadHandler(newHandlerLogger(t), db, blobStore, nil)
	if pub != nil {
		h.SetPublisher(pub)
	}

	router := chi.NewRouter()
	h.Register(router)

	return router
}

func uploadRPM(t *testing.T, router chi.Router, data []byte, repoName string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, buildRPMUploadRequest(t, data, repoName))

	return rec
}

func TestRPMUpload_QuotaExceeded_507_NothingPersisted(t *testing.T) {
	db := newMockDatabase()
	blobStore := newMockBlobStore()
	seedTestRepo(db, "repo-q", "quota-repo")
	db.setQuotaExceeded(true)

	pub := &recordingPublisher{}
	router := newRPMUploadRouter(t, db, blobStore, pub)

	rec := uploadRPM(t, router, buildMinimalRPM(), "quota-repo")
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("expected 507, got %d: %s", rec.Code, rec.Body.String())
	}

	if len(db.components) != 0 || len(blobStore.blobs) != 0 {
		t.Errorf("nothing may be persisted over quota: components=%d blobs=%d", len(db.components), len(blobStore.blobs))
	}

	if n := len(pub.snapshot()); n != 0 {
		t.Errorf("no webhook event expected, got %d", n)
	}
}

func TestRPMUpload_QuotaReservedOnSuccess_ReleasedOnDuplicate(t *testing.T) {
	db := newMockDatabase()
	blobStore := newMockBlobStore()
	seedTestRepo(db, "repo-q2", "quota-repo2")

	pub := &recordingPublisher{}
	router := newRPMUploadRouter(t, db, blobStore, pub)
	data := buildMinimalRPM()

	if rec := uploadRPM(t, router, data, "quota-repo2"); rec.Code != http.StatusOK {
		t.Fatalf("first upload: %d %s", rec.Code, rec.Body.String())
	}

	size, artifacts, reserves, releases := db.quotaState()
	if size != int64(len(data)) || artifacts != 1 || reserves != 1 || releases != 0 {
		t.Errorf("after upload: bytes=%d artifacts=%d reserve=%d release=%d", size, artifacts, reserves, releases)
	}

	if rec := uploadRPM(t, router, data, "quota-repo2"); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate upload: %d", rec.Code)
	}

	size, artifacts, _, _ = db.quotaState()
	if size != int64(len(data)) || artifacts != 1 {
		t.Errorf("duplicate must not leak quota: bytes=%d artifacts=%d", size, artifacts)
	}

	events := pub.snapshot()
	if len(events) != 1 || events[0].event != "artifact.uploaded" || events[0].repoID != "repo-q2" {
		t.Fatalf("expected one artifact.uploaded for repo UUID, got %+v", events)
	}
}

func TestRPMDelete_ReleasesQuotaAndEmitsDeleted(t *testing.T) {
	mock := newMockDatabase()
	db := rpmLookupDB{mock}
	blobStore := &deleteRecordingStore{MockBlobStore: newMockBlobStore()}
	seedTestRepo(mock, "repo-d", "delete-repo")

	pub := &recordingPublisher{}
	router := newRPMUploadRouter(t, db, blobStore, pub)
	data := buildMinimalRPM()

	if rec := uploadRPM(t, router, data, "delete-repo"); rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	var id string
	for k := range mock.components {
		id = k
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodDelete,
		"/api/v1/repositories/delete-repo/packages/"+id, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	size, artifacts, _, releases := mock.quotaState()
	if size != 0 || artifacts != 0 || releases != 1 {
		t.Errorf("delete must release the package size: bytes=%d artifacts=%d releases=%d", size, artifacts, releases)
	}

	events := pub.snapshot()
	if len(events) != 2 || events[1].event != "artifact.deleted" || events[1].repoID != "repo-d" {
		t.Fatalf("expected uploaded+deleted events, got %+v", events)
	}

	if got := blobStore.deletedIDs(); len(got) != 1 || len(got[0]) != 64 {
		t.Errorf("unreferenced blob must be deleted by sha256 once, got %v", got)
	}
}

type fakePackageDeleter struct {
	repo *models.Repository
	id   string
	res  *worker.DeletedPackage
	err  error
}

func (f *fakePackageDeleter) DeletePackage(_ context.Context, repo *models.Repository, id string, guard func(worker.DeletedPackage) error) (*worker.DeletedPackage, error) {
	f.repo, f.id = repo, id

	if f.err != nil {
		return nil, f.err
	}

	if err := guard(*f.res); err != nil {
		return nil, err
	}

	return f.res, nil
}

// The id-based package delete of every format but deb and rpm goes through
// the generic deleter: it deletes, maps not-found to 404 and an immutable
// release to 409, and answers 501 when none is wired.
func TestPackageDelete_GenericFormats(t *testing.T) {
	mock := newMockDatabase()
	db := rpmLookupDB{mock}
	mock.repositories["repo-npm"] = &models.Repository{ID: "repo-npm", Name: "npm-repo", Format: "npm", RepoType: "hosted"}
	mock.repositories["repo-imm"] = &models.Repository{ID: "repo-imm", Name: "imm-repo", Format: "npm", RepoType: "hosted", ImmutableReleases: true}

	del := func(h *api.RPMUploadHandler, repo string) *httptest.ResponseRecorder {
		router := chi.NewRouter()
		h.Register(router)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodDelete,
			"/api/v1/repositories/"+repo+"/packages/p-1", nil))

		return rec
	}

	newHandler := func() *api.RPMUploadHandler {
		return api.NewRPMUploadHandler(newHandlerLogger(t), db, newMockBlobStore(), nil)
	}

	if rec := del(newHandler(), "npm-repo"); rec.Code != http.StatusNotImplemented {
		t.Errorf("no deleter: got %d %s, want 501", rec.Code, rec.Body.String())
	}

	d := &fakePackageDeleter{res: &worker.DeletedPackage{ID: "p-1", Name: "left-pad", Version: "1.0.0", Rows: 1}}
	h := newHandler()
	h.SetPackageDeleter(d)

	if rec := del(h, "npm-repo"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"package_id":"p-1"`) {
		t.Fatalf("delete: got %d %s, want 200", rec.Code, rec.Body.String())
	}

	if d.repo == nil || d.repo.Name != "npm-repo" || d.id != "p-1" {
		t.Errorf("deleter called with %+v, %q", d.repo, d.id)
	}

	if rec := del(h, "imm-repo"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "IMMUTABLE_RELEASE") {
		t.Errorf("immutable release: got %d %s, want 409", rec.Code, rec.Body.String())
	}

	d.err = worker.ErrPackageNotFound
	if rec := del(h, "npm-repo"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown package: got %d, want 404", rec.Code)
	}
}

func TestRPMUpload_ConcurrentDuplicates_QuotaNetOne(t *testing.T) {
	db := newMockDatabase()
	blobStore := newMockBlobStore()
	seedTestRepo(db, "repo-c", "conc-quota")

	pub := &recordingPublisher{}
	router := newRPMUploadRouter(t, db, blobStore, pub)
	data := buildMinimalRPM()

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			uploadRPM(t, router, data, "conc-quota")
		})
	}

	wg.Wait()

	size, artifacts, _, _ := db.quotaState()
	if size != int64(len(data)) || artifacts != 1 {
		t.Errorf("losers must release their reservation: bytes=%d artifacts=%d", size, artifacts)
	}

	if n := len(pub.snapshot()); n != 1 {
		t.Errorf("exactly one artifact.uploaded expected, got %d", n)
	}

	if len(blobStore.blobs) != 1 {
		t.Errorf("exactly one blob expected, got %d", len(blobStore.blobs))
	}
}
