// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeCacheDB is a minimal database.Database backed by an in-memory map,
// exercising only the proxy cache entry methods ServeProxy actually calls.
type fakeCacheDB struct {
	database.Database

	entries map[string]*models.ProxyCacheEntry
}

func newFakeCacheDB() *fakeCacheDB {
	return &fakeCacheDB{entries: map[string]*models.ProxyCacheEntry{}}
}

func (f *fakeCacheDB) GetProxyCacheEntry(_ context.Context, repoID, cacheKey string) (*models.ProxyCacheEntry, error) {
	e, ok := f.entries[repoID+"|"+cacheKey]
	if !ok {
		return nil, nil
	}

	cp := *e

	return &cp, nil
}

func (f *fakeCacheDB) UpsertProxyCacheEntry(_ context.Context, entry *models.ProxyCacheEntry) error {
	cp := *entry
	f.entries[entry.RepositoryID+"|"+entry.CacheKey] = &cp

	return nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type failingReader struct {
	data []byte
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, io.ErrUnexpectedEOF
}

func newTestProxyHandler(t *testing.T, db database.Database) (*Handler, storage.BlobStore) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	blobStore, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("failed to create blob store: %v", err)
	}

	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true") // httptest upstreams listen on loopback

	return NewHandler(log, db, blobStore), blobStore
}

// TestServeProxy_CacheMissThenHit_RoundTrips verifies that a cache miss
// stores the blob under the persisted entry's BlobKey (content-addressed by
// sha256), so that the next request is served as a cache hit. This exercises
// miss -> persisted entry -> the entry's BlobKey resolving in the blob
// store -> hit.
func TestServeProxy_CacheMissThenHit_RoundTrips(t *testing.T) {
	const body = "hello from upstream"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	db := newFakeCacheDB()
	h, blobStore := newTestProxyHandler(t, db)

	repo := &models.Repository{
		ID:              "repo1",
		UpstreamURL:     upstream.URL,
		CacheTTLSeconds: 3600,
	}

	req1 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w1 := httptest.NewRecorder()
	h.ServeProxy(w1, req1, repo, "mykey", "/artifact.bin")

	if w1.Code != http.StatusOK {
		t.Fatalf("miss: status = %d, want 200", w1.Code)
	}

	if w1.Body.String() != body {
		t.Fatalf("miss: body = %q, want %q", w1.Body.String(), body)
	}

	entry := db.entries["repo1|mykey"]
	if entry == nil {
		t.Fatal("expected a cache entry to be persisted after a miss")
	}

	if entry.BlobKey == "" {
		t.Fatal("expected BlobKey to be set to a real blob id, got empty string")
	}

	blob, err := blobStore.Get(context.Background(), entry.BlobKey)
	if err != nil {
		t.Fatalf("blob store Get(%q) failed (BlobKey doesn't match what Put stored): %v", entry.BlobKey, err)
	}

	data, err := io.ReadAll(blob)
	_ = blob.Close()

	if err != nil {
		t.Fatalf("failed to read cached blob: %v", err)
	}

	if string(data) != body {
		t.Fatalf("stored blob content = %q, want %q", string(data), body)
	}

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	h.ServeProxy(w2, req2, repo, "mykey", "/artifact.bin")

	if w2.Code != http.StatusOK {
		t.Fatalf("hit: status = %d, want 200 (before the fix this was a 500)", w2.Code)
	}

	if w2.Body.String() != body {
		t.Fatalf("hit: body = %q, want %q", w2.Body.String(), body)
	}

	if got := db.entries["repo1|mykey"].HitCount; got != 1 {
		t.Fatalf("hit count = %d, want 1", got)
	}
}

func TestServeProxy_StreamFailureDoesNotCache(t *testing.T) {
	db := newFakeCacheDB()
	h, _ := newTestProxyHandler(t, db)
	h.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(&failingReader{data: []byte("partial")}),
			Request:    req,
		}, nil
	})

	repo := &models.Repository{ID: "repo1", UpstreamURL: "https://upstream.example"}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeProxy(w, req, repo, "mykey", "/artifact.bin")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "partial" {
		t.Fatalf("body = %q, want %q", got, "partial")
	}
	if entry := db.entries["repo1|mykey"]; entry != nil {
		t.Fatalf("cache entry persisted after failed stream: %+v", entry)
	}
}

func TestServeProxy_BlockedRepo(t *testing.T) {
	db := newFakeCacheDB()
	h, _ := newTestProxyHandler(t, db)

	repo := &models.Repository{ID: "repo1", Blocked: true}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeProxy(w, req, repo, "key1", "/some/path")

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestServeProxy_Upstream404_NoCacheEntryLeak(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	db := newFakeCacheDB()
	h, _ := newTestProxyHandler(t, db)

	repo := &models.Repository{
		ID:                      "repo1",
		UpstreamURL:             upstream.URL,
		NegativeCacheTTLSeconds: 60,
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeProxy(w, req, repo, "missing-key", "/notfound")

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}

	entry := db.entries["repo1|missing-key"]
	if entry == nil || entry.StatusCode != http.StatusNotFound {
		t.Fatal("expected a negative cache entry to be stored")
	}

	if entry.BlobKey != "" {
		t.Fatalf("negative cache entry should not reference a blob, got BlobKey=%q", entry.BlobKey)
	}
}

func TestServeProxy_NegativeCacheHit_Returns404(t *testing.T) {
	db := newFakeCacheDB()
	h, _ := newTestProxyHandler(t, db)
	db.entries["repo1|k"] = &models.ProxyCacheEntry{
		RepositoryID: "repo1", CacheKey: "k", StatusCode: http.StatusNotFound,
		ExpiresAt: time.Now().Add(time.Minute),
	}
	repo := &models.Repository{ID: "repo1", UpstreamURL: "http://unused.invalid"}
	w := httptest.NewRecorder()
	h.ServeProxy(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), repo, "k", "/x")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestServeProxy_BlocksLoopbackUpstreamByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer upstream.Close()

	db := newFakeCacheDB()
	h, _ := newTestProxyHandler(t, db)
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "false")

	repo := &models.Repository{ID: "repo1", UpstreamURL: upstream.URL}
	w := httptest.NewRecorder()
	h.ServeProxy(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil), repo, "k", "/x")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}
