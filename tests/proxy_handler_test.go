// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newProxyLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	return log
}

func TestProxyHandler_BlockedRepo(t *testing.T) {
	dir := t.TempDir()
	blobStore, _ := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	db := newMockDatabase()
	log := newProxyLogger(t)
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true") // httptest upstreams are loopback
	h := proxy.NewHandler(log, db, blobStore)

	repo := &models.Repository{ID: "repo1", Blocked: true}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeProxy(w, req, repo, "key1", "/some/path")

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestProxyHandler_CacheMiss_FetchFromUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	blobStore, _ := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	db := newMockDatabase()
	log := newProxyLogger(t)
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true") // httptest upstreams are loopback
	h := proxy.NewHandler(log, db, blobStore)

	repo := &models.Repository{
		ID:              "repo1",
		Blocked:         false,
		UpstreamURL:     upstream.URL,
		CacheTTLSeconds: 3600,
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeProxy(w, req, repo, "mykey", "/artifact.bin")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestProxyHandler_CacheMiss_Upstream404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	blobStore, _ := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	db := newMockDatabase()
	log := newProxyLogger(t)
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true") // httptest upstreams are loopback
	h := proxy.NewHandler(log, db, blobStore)

	repo := &models.Repository{
		ID:                      "repo1",
		Blocked:                 false,
		UpstreamURL:             upstream.URL,
		NegativeCacheTTLSeconds: 60,
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeProxy(w, req, repo, "missing-key", "/notfound")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestProxyHandler_CacheMiss_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	blobStore, _ := storage.NewFilesystemStore(filepath.Join(dir, "blobs"))
	db := newMockDatabase()
	log := newProxyLogger(t)
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true") // httptest upstreams are loopback
	h := proxy.NewHandler(log, db, blobStore)

	repo := &models.Repository{
		ID:          "repo1",
		Blocked:     false,
		UpstreamURL: upstream.URL,
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeProxy(w, req, repo, "error-key", "/error")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
