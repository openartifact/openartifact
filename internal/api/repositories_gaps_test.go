// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
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
	database.BaseMockDB

	repo        *models.Repository
	comp        *models.Component
	quarantined map[string]bool // keys reported quarantined
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

func (db *repoLifecycleDB) GetComponentByID(_ context.Context, id string) (*models.Component, error) {
	if db.comp != nil && db.comp.ID == id {
		return db.comp, nil
	}

	return nil, nil
}

func (db *repoLifecycleDB) IsArtifactQuarantined(_ context.Context, repoID string, keys []string) (bool, error) {
	for _, k := range keys {
		if repoID == db.repo.ID && db.quarantined[k] {
			return true, nil
		}
	}

	return false, nil
}

func (db *repoLifecycleDB) ListUnreferencedBlobs(_ context.Context, minAge time.Duration, _ int) ([]string, error) {
	db.minAge = minAge

	if !db.deleted {
		return nil, nil
	}

	out := db.orphans
	db.orphans = nil

	return out, nil
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
	router.ServeHTTP(rw, adminRequest(nil, http.MethodDelete, "/api/v1/repositories/evrepo", ""))

	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d; body = %s", rw.Code, rw.Body.String())
	}

	h.WaitBackground()

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
		comp:        &models.Component{ID: "pkg-1", RepositoryID: "repo-1", Filename: "hello_1.0_amd64.deb", SHA256: sha},
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
