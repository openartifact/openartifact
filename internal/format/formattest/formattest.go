// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package formattest provides in-memory fakes shared by the format handler
// tests: a content-addressed blob store, a recording webhook publisher and a
// quarantine lookup.
package formattest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errNoBlob = errors.New("formattest: blob not found")

// BlobStore is a minimal content-addressed in-memory storage.BlobStore. Blobs
// are keyed by the "sha256" metadata entry given to Put.
type BlobStore struct {
	mu    sync.Mutex
	Blobs map[string][]byte
}

// NewBlobStore returns an empty BlobStore.
func NewBlobStore() *BlobStore { return &BlobStore{Blobs: map[string][]byte{}} }

// Put stores data under metadata["sha256"].
func (m *BlobStore) Put(_ context.Context, data io.Reader, metadata map[string]string) (string, error) {
	b, err := io.ReadAll(data)
	if err != nil {
		return "", err
	}

	id := metadata["sha256"]

	m.mu.Lock()
	m.Blobs[id] = b
	m.mu.Unlock()

	return id, nil
}

// DiscardBlobStore is a BlobStore that reads and drops what it is given, for
// benchmarks that must measure a handler rather than the fake's own copy of
// every blob.
type DiscardBlobStore struct{ *BlobStore }

// NewDiscardBlobStore returns an empty DiscardBlobStore.
func NewDiscardBlobStore() *DiscardBlobStore { return &DiscardBlobStore{NewBlobStore()} }

// Put drains data and returns metadata["sha256"] without keeping anything.
func (m *DiscardBlobStore) Put(_ context.Context, data io.Reader, metadata map[string]string) (string, error) {
	if _, err := io.Copy(io.Discard, data); err != nil {
		return "", err
	}

	return metadata["sha256"], nil
}

// Get returns the blob stored under id.
func (m *BlobStore) Get(_ context.Context, id string) (io.ReadCloser, error) {
	m.mu.Lock()
	b, ok := m.Blobs[id]
	m.mu.Unlock()

	if !ok {
		return nil, errNoBlob
	}

	return io.NopCloser(bytes.NewReader(b)), nil
}

// Delete removes a blob.
func (m *BlobStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.Blobs, id)
	m.mu.Unlock()

	return nil
}

// Exists reports whether a blob exists.
func (m *BlobStore) Exists(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	_, ok := m.Blobs[id]
	m.mu.Unlock()

	return ok, nil
}

// GetMetadata returns no metadata.
func (m *BlobStore) GetMetadata(_ context.Context, _ string) (map[string]string, error) {
	return nil, nil //nolint:nilnil // no metadata is modelled
}

// List returns nothing.
func (m *BlobStore) List(_ context.Context, _ string, _ int) ([]string, string, error) {
	return nil, "", nil
}

// GetSize returns the blob size.
func (m *BlobStore) GetSize(_ context.Context, id string) (int64, error) {
	m.mu.Lock()
	b, ok := m.Blobs[id]
	m.mu.Unlock()

	if !ok {
		return 0, errNoBlob
	}

	return int64(len(b)), nil
}

// ExistsBySHA256 reports whether a blob with the digest exists.
func (m *BlobStore) ExistsBySHA256(_ context.Context, sha256 string) (string, bool, error) {
	m.mu.Lock()
	_, ok := m.Blobs[sha256]
	m.mu.Unlock()

	return sha256, ok, nil
}

// PresignURL is unsupported.
func (m *BlobStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", nil
}

// Event is one published webhook event.
type Event struct {
	RepoID  string
	Type    string
	Payload map[string]any
}

// Artifact returns the "artifact" object of the payload.
func (e Event) Artifact() map[string]any {
	a, _ := e.Payload["artifact"].(map[string]any)
	return a
}

// Publisher is a webhook.Publisher recording every event.
type Publisher struct {
	mu     sync.Mutex
	events []Event
}

// Dispatch records the event.
func (p *Publisher) Dispatch(_ context.Context, repoID, eventType string, payload map[string]any) {
	p.mu.Lock()
	p.events = append(p.events, Event{RepoID: repoID, Type: eventType, Payload: payload})
	p.mu.Unlock()
}

// Events returns a copy of the recorded events.
func (p *Publisher) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.events)
}

// Quarantine implements IsArtifactQuarantined over a set of quarantined keys;
// embed it in a database mock.
type Quarantine struct {
	Keys map[string]bool
}

// IsArtifactQuarantined reports whether any key is in the quarantined set.
func (q *Quarantine) IsArtifactQuarantined(_ context.Context, _ string, keys []string) (bool, error) {
	for _, k := range keys {
		if q.Keys[k] {
			return true, nil
		}
	}

	return false, nil
}

// DB is a database.Database fake serving one repository, with quarantine
// lookups and a zero blob reference count (so SafeDeleteBlob removes blobs).
// Embed it in a per-format mock that adds the format's package methods.
type DB struct {
	database.BaseMockDB
	Quarantine

	Repo *models.Repository
}

// NewDB returns a DB serving repository "myrepo" (ID "repo-1").
func NewDB() *DB {
	d := &DB{Repo: &models.Repository{ID: "repo-1", Name: "myrepo"}}
	d.Keys = map[string]bool{}

	return d
}

// GetRepositoryByName returns the repository when the name matches.
func (d *DB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if d.Repo != nil && d.Repo.Name == name {
		return d.Repo, nil
	}

	return nil, nil //nolint:nilnil // not found is (nil, nil) in the Database contract
}

// BlobReferenceCount always reports no references.
func (d *DB) BlobReferenceCount(_ context.Context, _ string) (int, error) { return 0, nil }

// NewLogger returns a quiet logger.
func NewLogger(tb testing.TB) *logger.Logger {
	tb.Helper()

	log, err := logger.New("error", "json", "stdout")
	if err != nil {
		tb.Fatalf("logger.New: %v", err)
	}

	return log
}

// Do routes req through a chi router mounted at /repos/{name} using register
// and returns the recorded response.
func Do(register func(chi.Router), req *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Route("/repos/{name}", register)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	return w
}

// Request builds a request with a background context.
func Request(method, target string, body io.Reader) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), method, target, body)
}
