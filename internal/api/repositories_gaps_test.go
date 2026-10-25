// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
)

type recordedEvent struct {
	repoID, event string
	payload       map[string]any
}

type recordingPublisher struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (p *recordingPublisher) Dispatch(_ context.Context, repoID, eventType string, payload map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.events = append(p.events, recordedEvent{repoID, eventType, payload})
}

func (p *recordingPublisher) all() []recordedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]recordedEvent(nil), p.events...)
}

// repoLifecycleDB models a repository whose delete orphans one blob.
type repoLifecycleDB struct {
	databasetest.BaseMockDB

	repo        *models.Repository
	blobs       map[string]*models.ComponentBlob // listing row id -> blob, all in repo
	quarantined map[string]bool                  // keys reported quarantined
	deleted     bool
	orphans     []string
	minAge      time.Duration
	targets     []*models.PermissionTarget
}

func (db *repoLifecycleDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return db.targets, nil
}

func (db *repoLifecycleDB) CreateRepositoryWithMembers(_ context.Context, repo *models.Repository, _ []string) error {
	repo.ID = "repo-new"
	db.repo = repo

	return nil
}

func (db *repoLifecycleDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if db.repo != nil && db.repo.Name == name {
		return db.repo, nil
	}

	return nil, nil
}

func (db *repoLifecycleDB) DeleteRepository(context.Context, string) error {
	db.deleted = true

	return nil
}

func (db *repoLifecycleDB) GetRepoComponentBlob(_ context.Context, repoID, id string) (*models.ComponentBlob, error) {
	if db.repo == nil || db.repo.ID != repoID {
		return nil, nil
	}

	return db.blobs[id], nil
}

func (db *repoLifecycleDB) IsArtifactQuarantined(_ context.Context, repoID string, keys []string) (bool, error) {
	for _, k := range keys {
		if repoID == db.repo.ID && db.quarantined[k] {
			return true, nil
		}
	}

	return false, nil
}

func (db *repoLifecycleDB) ListUnreferencedBlobs(_ context.Context, minAge time.Duration, _ string, _, _ int) ([]string, string, error) {
	db.minAge = minAge

	if !db.deleted {
		return nil, "", nil
	}

	out := db.orphans
	db.orphans = nil

	return out, "", nil
}

func (db *repoLifecycleDB) BlobReferenceCount(context.Context, string) (int, error) { return 0, nil }

// memBlobs is a BlobStore double that serves one blob and records deletes.
type memBlobs struct {
	storage.BlobStore

	mu      sync.Mutex
	content map[string]string
	deleted []string
}

func (s *memBlobs) Get(_ context.Context, id string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.content[id])), nil
}

func (s *memBlobs) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deleted = append(s.deleted, id)

	return nil
}

// waitUntil polls cond until it holds, failing the test after a deadline.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func (s *memBlobs) deletedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.deleted...)
}

func TestRepositoryCreateAndDeleteEmitEventsAndCollectOrphans(t *testing.T) {
	orphan := strings.Repeat("f", 64)
	db := &repoLifecycleDB{targets: adminTargets(), orphans: []string{orphan}}
	blobs := &memBlobs{}
	pub := &recordingPublisher{}

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithBlobStore(blobs).WithPublisher(pub)

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(nil, http.MethodPost, "/api/v1/repositories", `{"name":"evrepo","format":"raw"}`))

	if rw.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body = %s", rw.Code, rw.Body.String())
	}

	router := chi.NewRouter()
	router.Delete("/api/v1/repositories/{name}", h.handleDelete)

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, adminRequest(nil, http.MethodDelete, "/api/v1/repositories/evrepo?confirm=evrepo", ""))

	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d; body = %s", rw.Code, rw.Body.String())
	}

	waitUntil(t, "the orphan sweep to delete the blob", func() bool { return len(blobs.deletedIDs()) > 0 })

	events := pub.all()
	if len(events) != 2 || events[0].event != "repository.created" || events[1].event != "repository.deleted" {
		t.Fatalf("events = %+v, want repository.created then repository.deleted", events)
	}

	for _, e := range events {
		if e.repoID != "repo-new" {
			t.Errorf("%s published for repo %q, want the repository UUID", e.event, e.repoID)
		}

		if e.payload["repository"] != "evrepo" || e.payload["format"] != "raw" || e.payload["event"] != e.event {
			t.Errorf("bad payload for %s: %v", e.event, e.payload)
		}
	}

	if got := blobs.deletedIDs(); len(got) != 1 || got[0] != orphan {
		t.Errorf("orphan blobs deleted after repository delete = %v, want [%s]", got, orphan)
	}

	if db.minAge != storage.OrphanMinAge {
		t.Errorf("orphan minAge = %v, want %v", db.minAge, storage.OrphanMinAge)
	}
}

func TestRepositoryDownloadHonoursQuarantine(t *testing.T) {
	sha := strings.Repeat("a", 64)
	db := &repoLifecycleDB{
		targets:     adminTargets(),
		repo:        &models.Repository{ID: "repo-1", Name: "pkgs", Format: "deb"},
		blobs:       map[string]*models.ComponentBlob{"pkg-1": {ID: "pkg-1", Format: "deb", Filename: "hello_1.0_amd64.deb", SHA256: sha}},
		quarantined: map[string]bool{},
	}
	blobs := &memBlobs{content: map[string]string{sha: "DEB-BYTES"}}

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithBlobStore(blobs)

	router := chi.NewRouter()
	router.Get("/api/v1/repositories/{name}/packages/{id}/download", h.handleDownloadPackage)

	get := func() *httptest.ResponseRecorder {
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, "/api/v1/repositories/pkgs/packages/pkg-1/download", ""))

		return rw
	}

	if rw := get(); rw.Code != http.StatusOK || rw.Body.String() != "DEB-BYTES" {
		t.Fatalf("clean package: status %d body %q", rw.Code, rw.Body.String())
	}

	// The UI quarantines a package by its row id.
	db.quarantined["pkg-1"] = true

	rw := get()
	if rw.Code != http.StatusForbidden || !strings.Contains(rw.Body.String(), "ARTIFACT_QUARANTINED") {
		t.Fatalf("quarantined package: status %d body %q, want 403 ARTIFACT_QUARANTINED", rw.Code, rw.Body.String())
	}

	if strings.Contains(rw.Body.String(), "DEB-BYTES") {
		t.Error("quarantined artifact bytes leaked")
	}
}

func TestRepositoryDownloadByFormat(t *testing.T) {
	sha := strings.Repeat("b", 64)
	body := "BLOB-BYTES"

	cases := []struct {
		name         string
		blob         models.ComponentBlob
		wantStatus   int
		wantFilename string
		wantType     string
		wantCode     string
	}{
		{
			name:         "npm tarball keeps its name and media type",
			blob:         models.ComponentBlob{Format: "npm", Filename: "widget-1.0.0.tgz", SHA256: sha, Size: int64(len(body)), MediaType: "application/gzip"},
			wantStatus:   http.StatusOK,
			wantFilename: "widget-1.0.0.tgz",
			wantType:     "application/gzip",
		},
		{
			name:         "maven repo path is reduced to its basename",
			blob:         models.ComponentBlob{Format: "maven", Filename: "com/acme/x/1.0/x-1.0.jar", SHA256: sha, Size: int64(len(body))},
			wantStatus:   http.StatusOK,
			wantFilename: "x-1.0.jar",
			wantType:     "application/octet-stream",
		},
		{
			name:         "raw path traversal cannot escape the basename",
			blob:         models.ComponentBlob{Format: "raw", Filename: "../../etc/passwd", SHA256: sha, Size: int64(len(body))},
			wantStatus:   http.StatusOK,
			wantFilename: "passwd",
			wantType:     "application/octet-stream",
		},
		{
			name:         "docker tag downloads the manifest named after its digest",
			blob:         models.ComponentBlob{Format: "docker", Filename: "sha256:" + strings.Repeat("c", 64), SHA256: sha, Size: int64(len(body)), MediaType: "application/vnd.oci.image.manifest.v1+json"},
			wantStatus:   http.StatusOK,
			wantFilename: "sha256_" + strings.Repeat("c", 64) + ".json",
			wantType:     "application/vnd.oci.image.manifest.v1+json",
		},
		{
			name:         "conan reference keeps every part",
			blob:         models.ComponentBlob{Format: "conan", Filename: "zlib/1.3@_/_#abc", SHA256: sha, Size: int64(len(body))},
			wantStatus:   http.StatusOK,
			wantFilename: "zlib_1.3@___#abc",
			wantType:     "application/octet-stream",
		},
		{
			name:         "invalid stored media type falls back to octet-stream",
			blob:         models.ComponentBlob{Format: "pypi", Filename: "a-1.whl", SHA256: sha, Size: int64(len(body)), MediaType: "not a media type"},
			wantStatus:   http.StatusOK,
			wantFilename: "a-1.whl",
			wantType:     "application/octet-stream",
		},
		{
			name:       "row without a blob is not found",
			blob:       models.ComponentBlob{Format: "conan", Filename: "zlib/1.3@_/_#abc"},
			wantStatus: http.StatusNotFound,
			wantCode:   "not_found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob := tc.blob
			blob.ID = "row-1"

			db := &repoLifecycleDB{
				targets: adminTargets(),
				repo:    &models.Repository{ID: "repo-1", Name: "pkgs", Format: blob.Format},
				blobs:   map[string]*models.ComponentBlob{"row-1": &blob},
			}

			log := newFixTestLogger(t)
			h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithBlobStore(&memBlobs{content: map[string]string{sha: body}})

			router := chi.NewRouter()
			router.Get("/api/v1/repositories/{name}/packages/{id}/download", h.handleDownloadPackage)

			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, "/api/v1/repositories/pkgs/packages/row-1/download", ""))

			if rw.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tc.wantStatus, rw.Body.String())
			}

			if tc.wantStatus != http.StatusOK {
				if !strings.Contains(rw.Body.String(), tc.wantCode) {
					t.Fatalf("body = %s, want code %q", rw.Body.String(), tc.wantCode)
				}

				return
			}

			if rw.Body.String() != body {
				t.Errorf("body = %q, want %q", rw.Body.String(), body)
			}

			if got := rw.Header().Get("Content-Type"); got != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
			}

			if got := rw.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
				t.Errorf("Content-Length = %q, want %d", got, len(body))
			}

			_, params, err := mime.ParseMediaType(rw.Header().Get("Content-Disposition"))
			if err != nil || params["filename"] != tc.wantFilename {
				t.Errorf("Content-Disposition = %q (filename %q, err %v), want attachment filename %q",
					rw.Header().Get("Content-Disposition"), params["filename"], err, tc.wantFilename)
			}
		})
	}
}

func TestRepositoryDownloadUnknownOrForeignIDIsNotFound(t *testing.T) {
	db := &repoLifecycleDB{
		targets: adminTargets(),
		repo:    &models.Repository{ID: "repo-1", Name: "pkgs", Format: "raw"},
		blobs:   map[string]*models.ComponentBlob{},
	}

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithBlobStore(&memBlobs{})

	router := chi.NewRouter()
	router.Get("/api/v1/repositories/{name}/packages/{id}/download", h.handleDownloadPackage)

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, adminRequest(nil, http.MethodGet, "/api/v1/repositories/pkgs/packages/other-repo-row/download", ""))

	if rw.Code != http.StatusNotFound || !strings.Contains(rw.Body.String(), "not_found") {
		t.Fatalf("status %d body %q, want 404 not_found", rw.Code, rw.Body.String())
	}
}
