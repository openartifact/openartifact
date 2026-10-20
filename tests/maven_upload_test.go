// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

func TestMavenUpload_ReleasePublish_New(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()

	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, bs, nil)
	router := chi.NewRouter()
	h.Register(router)

	body := bytes.NewReader([]byte("fake-jar-bytes"))
	req := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}

	found := false

	for _, a := range db.mavenArtifacts {
		if a.RepoPath == "com/example/myapp/1.0.0/myapp-1.0.0.jar" {
			found = true
		}
	}

	if !found {
		t.Error("artifact not stored in DB")
	}
}

func TestMavenUpload_ReleaseRepublish_Conflict(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()

	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}
	db.mavenArtifacts["art-1"] = &models.MavenArtifact{
		ID:           "art-1",
		RepositoryID: "repo-1",
		RepoPath:     "com/example/myapp/1.0.0/myapp-1.0.0.jar",
		SHA256:       "different-sha",
		IsSnapshot:   false,
	}

	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, bs, nil)
	router := chi.NewRouter()
	h.Register(router)

	body := bytes.NewReader([]byte("different-content"))
	req := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

func TestMavenUpload_ReleaseRepublish_Idempotent(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()

	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

	content := []byte("same-content")
	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, bs, nil)
	router := chi.NewRouter()
	h.Register(router)

	req1 := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar", bytes.NewReader(content))
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusCreated {
		t.Fatalf("first PUT expected 201, got %d", w1.Code)
	}

	req2 := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar", bytes.NewReader(content))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("idempotent re-PUT expected 200, got %d", w2.Code)
	}
}

func TestMavenUpload_SnapshotPublish(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()

	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, bs, nil)
	router := chi.NewRouter()
	h.Register(router)

	body := bytes.NewReader([]byte("snapshot-jar"))
	req := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0-SNAPSHOT/myapp-1.0.0-20260426.120000-3.jar", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}

	var stored *models.MavenArtifact
	for _, a := range db.mavenArtifacts {
		stored = a
	}

	if stored == nil {
		t.Fatal("no artifact stored")
	}

	if stored.SnapshotTimestamp == "" {
		t.Error("expected snapshot_timestamp to be populated")
	}
}

func TestMavenUpload_ChecksumSibling_NoOp(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()

	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, bs, nil)
	router := chi.NewRouter()
	h.Register(router)

	for _, suffix := range []string{".sha1", ".md5", ".sha256", ".sha512"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar"+suffix, bytes.NewReader([]byte("abc")))
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("checksum PUT %s expected 200, got %d", suffix, w.Code)
		}
	}

	if len(db.mavenArtifacts) != 0 {
		t.Errorf("expected no rows created for checksum files, got %d", len(db.mavenArtifacts))
	}
}

func TestMavenUpload_AscPassthrough(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()

	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, bs, nil)
	router := chi.NewRouter()
	h.Register(router)

	body := bytes.NewReader([]byte("gpg-signature-data"))
	req := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar.asc", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}

	var stored *models.MavenArtifact
	for _, a := range db.mavenArtifacts {
		stored = a
	}

	if stored == nil {
		t.Fatal("no artifact stored for .asc file")
	}

	// Maven layout: the signature of myapp-1.0.0.jar is myapp-1.0.0.jar.asc,
	// whose extension is "jar.asc" (as written in maven-metadata.xml).
	if stored.Extension != "jar.asc" {
		t.Errorf("extension = %q, want %q", stored.Extension, "jar.asc")
	}
}

type mavenRecordingPublisher struct {
	events []string
	paths  []string
}

func (p *mavenRecordingPublisher) Dispatch(_ context.Context, _ string, eventType string, payload map[string]any) {
	p.events = append(p.events, eventType)

	if a, ok := payload["artifact"].(map[string]any); ok {
		if path, ok := a["path"].(string); ok {
			p.paths = append(p.paths, path)
		}
	}
}

func newMavenUploadRouter(t *testing.T, db *MockDatabase, pub *mavenRecordingPublisher) chi.Router {
	t.Helper()

	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

	log, _ := logger.New("info", "json", "stdout")
	h := api.NewMavenUploadHandler(log, db, newMockBlobStore(), nil)

	if pub != nil {
		h.SetPublisher(pub)
	}

	router := chi.NewRouter()
	h.Register(router)

	return router
}

func mavenDo(router chi.Router, method, path string, body []byte) int {
	req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w.Code
}

func TestMavenUpload_QuotaAndWebhooks(t *testing.T) {
	db := newMockDatabase()
	pub := &mavenRecordingPublisher{}
	router := newMavenUploadRouter(t, db, pub)

	const path = "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar"

	if code := mavenDo(router, "PUT", path, []byte("jar-bytes")); code != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201", code)
	}

	if b, n, _, _ := db.quotaState(); b != 9 || n != 1 {
		t.Errorf("quota after upload = (%d,%d), want (9,1)", b, n)
	}

	// Identical re-publish and checksum sidecars must not count again.
	if code := mavenDo(router, "PUT", path, []byte("jar-bytes")); code != http.StatusOK {
		t.Fatalf("re-PUT = %d, want 200", code)
	}

	mavenDo(router, "PUT", path+".sha1", []byte("abc"))

	if b, n, _, _ := db.quotaState(); b != 9 || n != 1 {
		t.Errorf("quota after duplicate = (%d,%d), want (9,1)", b, n)
	}

	// Different content for a release conflicts and releases its reservation.
	if code := mavenDo(router, "PUT", path, []byte("other")); code != http.StatusConflict {
		t.Fatalf("conflicting PUT = %d, want 409", code)
	}

	if b, n, _, _ := db.quotaState(); b != 9 || n != 1 {
		t.Errorf("quota after conflict = (%d,%d), want (9,1)", b, n)
	}

	if code := mavenDo(router, "DELETE", path, nil); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", code)
	}

	if b, n, _, _ := db.quotaState(); b != 0 || n != 0 {
		t.Errorf("quota after delete = (%d,%d), want (0,0)", b, n)
	}

	want := []string{"artifact.uploaded", "artifact.deleted"}
	if len(pub.events) != 2 || pub.events[0] != want[0] || pub.events[1] != want[1] {
		t.Errorf("events = %v, want %v", pub.events, want)
	}
}

func TestMavenUpload_QuotaExceeded(t *testing.T) {
	db := newMockDatabase()
	db.setQuotaExceeded(true)

	pub := &mavenRecordingPublisher{}
	router := newMavenUploadRouter(t, db, pub)

	code := mavenDo(router, "PUT", "/repos/maven/maven-repo/com/example/myapp/1.0.0/myapp-1.0.0.jar", []byte("jar"))
	if code != http.StatusInsufficientStorage {
		t.Errorf("PUT = %d, want 507", code)
	}

	if len(db.mavenArtifacts) != 0 || len(pub.events) != 0 {
		t.Error("nothing must be stored or published when quota is exceeded")
	}
}
