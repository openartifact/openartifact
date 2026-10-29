// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

type npmRecordedEvent struct {
	repoID  string
	event   string
	payload map[string]any
}

type npmRecordingPublisher struct {
	mu     sync.Mutex
	events []npmRecordedEvent
}

func (p *npmRecordingPublisher) Dispatch(_ context.Context, repoID, eventType string, payload map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.events = append(p.events, npmRecordedEvent{repoID: repoID, event: eventType, payload: payload})
}

func (p *npmRecordingPublisher) snapshot() []npmRecordedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]npmRecordedEvent(nil), p.events...)
}

func setupUploadRouterWithPublisher(db *MockDatabase, bs *MockBlobStore, pub webhook.Publisher) chi.Router {
	log, _ := logger.New("info", "json", "stdout")
	h := api.NewNPMUploadHandler(log, db, bs, nil)
	h.SetPublisher(pub)

	router := chi.NewRouter()
	router.Use(authenticateTestBearer)
	h.Register(router)

	return router
}

func npmPublish(t *testing.T, router http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/repos/myrepo/lodash", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w
}

func TestNPMPublish_ReservesQuotaAndEmitsUploaded(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo", Format: "npm"}

	pub := &npmRecordingPublisher{}
	router := setupUploadRouterWithPublisher(db, bs, pub)

	w := npmPublish(t, router, buildPublishBody(t, "lodash", "1.0.0"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	bytesReserved, artifacts, reserveCalls, releaseCalls := db.quotaState()
	if bytesReserved <= 0 || artifacts != 1 || reserveCalls != 1 || releaseCalls != 0 {
		t.Errorf("quota state = (%d bytes, %d artifacts, %d reserve, %d release), want >0 bytes, 1 artifact, 1 reserve, 0 release",
			bytesReserved, artifacts, reserveCalls, releaseCalls)
	}

	events := pub.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}

	ev := events[0]
	if ev.repoID != "repo-1" || ev.event != webhook.EventArtifactUploaded {
		t.Errorf("event = %s for repo %q, want %s for repo-1", ev.event, ev.repoID, webhook.EventArtifactUploaded)
	}

	art, _ := ev.payload["artifact"].(map[string]any)
	if art["name"] != "lodash" || art["version"] != "1.0.0" || art["path"] != "lodash-1.0.0.tgz" || art["sha256"] == "" {
		t.Errorf("artifact payload = %v", art)
	}
}

func TestNPMPublish_QuotaExceeded_507_NoStateNoEvent(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo", Format: "npm"}
	db.setQuotaExceeded(true)

	pub := &npmRecordingPublisher{}
	router := setupUploadRouterWithPublisher(db, bs, pub)

	w := npmPublish(t, router, buildPublishBody(t, "lodash", "1.0.0"))
	if w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body: %s", w.Code, w.Body.String())
	}

	if pkg, _ := db.GetNPMPackageByName(context.Background(), "repo-1", "lodash"); pkg != nil {
		t.Error("package must be rolled back when the quota is exceeded")
	}

	if len(bs.blobs) != 0 {
		t.Errorf("blobs stored = %d, want 0", len(bs.blobs))
	}

	if bytesNet, artifacts, reserveCalls, releaseCalls := db.quotaState(); bytesNet != 0 || artifacts != 0 || reserveCalls != 0 ||
		releaseCalls != 0 {
		t.Errorf(
			"quota = (%d, %d, %d reserve, %d release), want all zero (nothing reserved on refusal)",
			bytesNet,
			artifacts,
			reserveCalls,
			releaseCalls,
		)
	}

	if n := len(pub.snapshot()); n != 0 {
		t.Errorf("events = %d, want 0", n)
	}
}

func TestNPMPublish_Republish409_NoQuotaNoEvent(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo", Format: "npm"}

	pub := &npmRecordingPublisher{}
	router := setupUploadRouterWithPublisher(db, bs, pub)
	body := buildPublishBody(t, "lodash", "1.0.0")

	if w := npmPublish(t, router, body); w.Code != http.StatusCreated {
		t.Fatalf("first publish status = %d", w.Code)
	}

	if w := npmPublish(t, router, body); w.Code != http.StatusConflict {
		t.Fatalf("republish status = %d, want 409", w.Code)
	}

	bytesNet, artifacts, reserveCalls, releaseCalls := db.quotaState()
	if artifacts != 1 || bytesNet <= 0 || reserveCalls != 1 || releaseCalls != 0 {
		t.Errorf(
			"quota = (%d bytes, %d artifacts, %d reserve, %d release), want one stored artifact and no extra reservation",
			bytesNet,
			artifacts,
			reserveCalls,
			releaseCalls,
		)
	}

	if n := len(pub.snapshot()); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

func seedNPMVersionWithBlob(t *testing.T, db *MockDatabase, bs *MockBlobStore, key, id, version string) int64 {
	t.Helper()

	payload := []byte("tarball-bytes-" + version)
	sha := "sha-" + version

	if _, err := bs.Put(context.Background(), bytes.NewReader(payload), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("blob put: %v", err)
	}

	db.npmVersions[key] = &models.NPMVersion{
		ID: id, PackageID: "pkg-1", Version: version,
		TarballFilename: "lodash-" + version + ".tgz",
		BlobSHA256:      sha,
		CreatedAt:       time.Now().Add(-time.Hour),
	}

	return int64(len(payload))
}

func TestNPMUnpublish_ReleasesQuotaAndEmitsDeleted(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo", Format: "npm"}
	db.npmPackages["pkg-1"] = &models.NPMPackage{ID: "pkg-1", RepositoryID: "repo-1", FullName: "lodash", Name: "lodash"}
	size1 := seedNPMVersionWithBlob(t, db, bs, "pkg-1:1.0.0", "v1", "1.0.0")
	size2 := seedNPMVersionWithBlob(t, db, bs, "pkg-1:2.0.0", "v2", "2.0.0")

	pub := &npmRecordingPublisher{}
	router := setupUploadRouterWithPublisher(db, bs, pub)

	// Single version: DELETE of that version's tarball.
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodDelete,
		"/repos/myrepo/lodash/-/lodash-1.0.0.tgz/-rev/1-abc",
		nil,
	)
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("single unpublish status = %d, want 200", w.Code)
	}

	events := pub.snapshot()
	if len(events) != 1 || events[0].event != webhook.EventArtifactDeleted {
		t.Fatalf("events after single unpublish = %v", events)
	}

	art, _ := events[0].payload["artifact"].(map[string]any)
	if art["version"] != "1.0.0" || art["path"] != "lodash-1.0.0.tgz" || art["size"] != size1 {
		t.Errorf("deleted payload = %v, want version 1.0.0 size %d", art, size1)
	}

	// Whole package: remaining version released too.
	req = httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/repos/myrepo/lodash", nil)
	req.Header.Set("Authorization", "Bearer token")

	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("whole-package unpublish status = %d, want 200", w.Code)
	}

	if n := len(pub.snapshot()); n != 2 {
		t.Errorf("events = %d, want 2 (one per removed version)", n)
	}

	// Net state starts at zero (versions were seeded directly), so the two
	// releases leave exactly -(size1+size2) bytes and -2 artifacts.
	bytesNet, artifacts, _, releaseCalls := db.quotaState()
	if releaseCalls != 2 || artifacts != -2 || bytesNet != -(size1+size2) {
		t.Errorf("quota = (%d bytes, %d artifacts, %d releases), want (%d, -2, 2)", bytesNet, artifacts, releaseCalls, -(size1 + size2))
	}
}
