// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

type fakeAuditDB struct {
	MockDatabase

	mu     sync.Mutex
	events []*models.AuditLog
}

func (f *fakeAuditDB) InsertAuditLog(_ context.Context, e *models.AuditLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.events = append(f.events, e)

	return nil
}

func (f *fakeAuditDB) recorded() []*models.AuditLog {
	f.mu.Lock()
	defer f.mu.Unlock()

	cp := make([]*models.AuditLog, len(f.events))
	copy(cp, f.events)

	return cp
}

func newFakeAuditLogger(db *fakeAuditDB) *audit.Logger {
	log, _ := logger.New("error", "json", "stderr")
	reg := newAuditTestRegistry()

	return audit.NewLogger(db, log, reg, 100)
}

func waitForEvents(db *fakeAuditDB, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(db.recorded()) >= want {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return false
}

func setupAuditRouter(al *audit.Logger) chi.Router {
	router := chi.NewRouter()
	sub := chi.NewRouter()
	sub.Use(middleware.AuditMiddleware(al))
	sub.HandleFunc("/repos/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Mount("/", sub)

	return router
}

func TestAuditMiddleware_POST_EmitsEvent(t *testing.T) {
	db := &fakeAuditDB{MockDatabase: *newMockDatabase()}

	al := newFakeAuditLogger(db)
	defer func() { _ = al.Shutdown(context.Background()) }()

	router := setupAuditRouter(al)
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/repos/myrepo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if !waitForEvents(db, 1, 2*time.Second) {
		t.Fatal("expected 1 audit event for POST, got 0")
	}

	evs := db.recorded()
	if evs[0].Action != "http.request" {
		t.Errorf("unexpected action: %s", evs[0].Action)
	}
}

func TestAuditMiddleware_PUT_EmitsEvent(t *testing.T) {
	db := &fakeAuditDB{MockDatabase: *newMockDatabase()}

	al := newFakeAuditLogger(db)
	defer func() { _ = al.Shutdown(context.Background()) }()

	router := setupAuditRouter(al)
	req := httptest.NewRequestWithContext(context.Background(), "PUT", "/repos/myrepo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if !waitForEvents(db, 1, 2*time.Second) {
		t.Fatal("expected 1 audit event for PUT, got 0")
	}
}

func TestAuditMiddleware_DELETE_EmitsEvent(t *testing.T) {
	db := &fakeAuditDB{MockDatabase: *newMockDatabase()}

	al := newFakeAuditLogger(db)
	defer func() { _ = al.Shutdown(context.Background()) }()

	router := setupAuditRouter(al)
	req := httptest.NewRequestWithContext(context.Background(), "DELETE", "/repos/myrepo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if !waitForEvents(db, 1, 2*time.Second) {
		t.Fatal("expected 1 audit event for DELETE, got 0")
	}
}

func TestAuditMiddleware_GET_NoEvent(t *testing.T) {
	db := &fakeAuditDB{MockDatabase: *newMockDatabase()}

	al := newFakeAuditLogger(db)
	defer func() { _ = al.Shutdown(context.Background()) }()

	router := setupAuditRouter(al)
	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/myrepo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Shutdown drains every queued event, so none can arrive after it returns.
	if err := al.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if len(db.recorded()) != 0 {
		t.Errorf("expected 0 audit events for GET, got %d", len(db.recorded()))
	}
}

func TestAuditMiddleware_ErrorStatus_RecordsDetails(t *testing.T) {
	db := &fakeAuditDB{MockDatabase: *newMockDatabase()}

	al := newFakeAuditLogger(db)
	defer func() { _ = al.Shutdown(context.Background()) }()

	router := chi.NewRouter()
	sub := chi.NewRouter()
	sub.Use(middleware.AuditMiddleware(al))
	sub.HandleFunc("/repos/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	router.Mount("/", sub)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/repos/myrepo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if !waitForEvents(db, 1, 2*time.Second) {
		t.Fatal("expected 1 audit event for POST with error status")
	}

	evs := db.recorded()
	var envelope map[string]any
	if err := json.Unmarshal(evs[0].DetailsJSON, &envelope); err != nil {
		t.Fatalf("failed to unmarshal details_json: %v", err)
	}

	msg, ok := envelope["message"].(string)
	if !ok {
		t.Fatalf("expected message field in details_json, got %v", envelope)
	}

	var details map[string]any
	if err := json.Unmarshal([]byte(msg), &details); err != nil {
		t.Fatalf("failed to unmarshal nested message: %v", err)
	}

	if details["error"] != true {
		t.Errorf("expected error=true for 500 response, got %v", details["error"])
	}

	if status, ok := details["status"].(float64); !ok || status != http.StatusInternalServerError {
		t.Errorf("expected status=500 for 500 response, got %v", details["status"])
	}
}
