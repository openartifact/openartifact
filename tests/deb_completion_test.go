// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	debformat "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

func TestCreateDebianDistribution(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	dist := &models.DebianDistribution{
		RepositoryID: "repo-1",
		Name:         "bookworm",
		Description:  "Debian 12",
		Codename:     "12",
	}

	err := mockDB.CreateDebianDistribution(ctx, dist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if dist.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	if _, ok := mockDB.debianDistributions[dist.ID]; !ok {
		t.Fatal("expected distribution to be stored")
	}
}

func TestGetDebianDistributionsByRepo(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	dist1 := &models.DebianDistribution{
		ID:           "dist-1",
		RepositoryID: "repo-1",
		Name:         "bookworm",
	}
	dist2 := &models.DebianDistribution{
		ID:           "dist-2",
		RepositoryID: "repo-1",
		Name:         "bullseye",
	}
	dist3 := &models.DebianDistribution{
		ID:           "dist-3",
		RepositoryID: "repo-2",
		Name:         "bookworm",
	}

	mockDB.debianDistributions["dist-1"] = dist1
	mockDB.debianDistributions["dist-2"] = dist2
	mockDB.debianDistributions["dist-3"] = dist3

	result, err := mockDB.GetDebianDistributionsByRepo(ctx, "repo-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 distributions, got %d", len(result))
	}
}

func TestCreateDebianComponent(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	comp := &models.DebianComponent{
		DistributionID: "dist-1",
		Name:           "main",
		Description:    "Main component",
	}

	err := mockDB.CreateDebianComponent(ctx, comp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if comp.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	if _, ok := mockDB.debianComponents[comp.ID]; !ok {
		t.Fatal("expected component to be stored")
	}
}

func TestGetDebianComponentsByDist(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	comp1 := &models.DebianComponent{
		ID:             "comp-1",
		DistributionID: "dist-1",
		Name:           "main",
	}
	comp2 := &models.DebianComponent{
		ID:             "comp-2",
		DistributionID: "dist-1",
		Name:           "universe",
	}
	comp3 := &models.DebianComponent{
		ID:             "comp-3",
		DistributionID: "dist-2",
		Name:           "main",
	}

	mockDB.debianComponents["comp-1"] = comp1
	mockDB.debianComponents["comp-2"] = comp2
	mockDB.debianComponents["comp-3"] = comp3

	result, err := mockDB.GetDebianComponentsByDist(ctx, "dist-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 components, got %d", len(result))
	}
}

func TestCreateDebianArchitecture(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	arch := &models.DebianArchitecture{
		DistributionID: "dist-1",
		Name:           "amd64",
	}

	err := mockDB.CreateDebianArchitecture(ctx, arch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if arch.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	if _, ok := mockDB.debianArchitectures[arch.ID]; !ok {
		t.Fatal("expected architecture to be stored")
	}
}

func TestGetDebianArchitecturesByDist(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()

	arch1 := &models.DebianArchitecture{
		ID:             "arch-1",
		DistributionID: "dist-1",
		Name:           "amd64",
	}
	arch2 := &models.DebianArchitecture{
		ID:             "arch-2",
		DistributionID: "dist-1",
		Name:           "arm64",
	}
	arch3 := &models.DebianArchitecture{
		ID:             "arch-3",
		DistributionID: "dist-2",
		Name:           "amd64",
	}

	mockDB.debianArchitectures["arch-1"] = arch1
	mockDB.debianArchitectures["arch-2"] = arch2
	mockDB.debianArchitectures["arch-3"] = arch3

	result, err := mockDB.GetDebianArchitecturesByDist(ctx, "dist-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 architectures, got %d", len(result))
	}
}

func TestDebHTTPHandler_ServeRelease(t *testing.T) {
	ctx := context.Background()
	mockDB := newMockDatabase()
	mockBlobStore := newMockBlobStore()

	repo := &models.Repository{
		ID:     "repo-1",
		Name:   "test-repo",
		Format: "deb",
	}
	mockDB.repositories["repo-1"] = repo

	releaseContent := []byte(`Origin: openartifact
Label: openartifact
Suite: bookworm
Version: 12
Codename: bookworm
Date: Mon, 21 Apr 2026 00:00:00 UTC
Architectures: amd64 arm64
Components: main universe
`)

	blobID, err := mockBlobStore.Put(ctx, bytes.NewReader(releaseContent), nil)
	if err != nil {
		t.Fatalf("failed to store blob: %v", err)
	}

	metadata := &models.RepoMetadata{
		ID:           "meta-1",
		RepositoryID: "repo-1",
		Path:         "dists/bookworm/Release",
		BlobSHA256:   blobID,
		BlobSize:     int64(len(releaseContent)),
	}
	mockDB.repoMetadata["repo-1"] = make(map[string]*models.RepoMetadata)
	mockDB.repoMetadata["repo-1"]["dists/bookworm/Release"] = metadata

	log, _ := logger.New("error", "console", "")
	_ = debformat.NewHTTPHandler(log, mockDB, mockBlobStore)

	router := chi.NewRouter()
	router.HandleFunc("/repos/{name}/dists/{dist}/Release", func(w http.ResponseWriter, r *http.Request) {
		repoID, err := mockDB.GetRepositoryByName(r.Context(), chi.URLParam(r, "name"))
		if err != nil || repoID == nil {
			http.NotFound(w, r)
			return
		}

		meta, err := mockDB.GetRepoMetadataByPath(r.Context(), repoID.ID, "dists/bookworm/Release")
		if err != nil || meta == nil {
			http.NotFound(w, r)
			return
		}

		reader, err := mockBlobStore.Get(r.Context(), meta.BlobSHA256)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		defer func() { _ = reader.Close() }()

		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"`+meta.BlobSHA256+`"`)
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.Copy(w, reader)
	})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/test-repo/dists/bookworm/Release", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if !bytes.Contains(w.Body.Bytes(), []byte("openartifact")) {
		t.Errorf("expected response to contain 'openartifact', got %s", w.Body.String())
	}
}

func TestDebHTTPHandler_ServeRelease_NotFound(t *testing.T) {
	mockDB := newMockDatabase()
	mockBlobStore := newMockBlobStore()

	log, _ := logger.New("error", "console", "")
	_ = debformat.NewHTTPHandler(log, mockDB, mockBlobStore)

	router := chi.NewRouter()
	router.HandleFunc("/repos/{name}/dists/{dist}/Release", func(w http.ResponseWriter, r *http.Request) {
		repoID, err := mockDB.GetRepositoryByName(r.Context(), chi.URLParam(r, "name"))
		if err != nil || repoID == nil {
			http.NotFound(w, r)
			return
		}

		meta, err := mockDB.GetRepoMetadataByPath(r.Context(), repoID.ID, "dists/bookworm/Release")
		if err != nil || meta == nil {
			http.NotFound(w, r)
			return
		}

		reader, err := mockBlobStore.Get(r.Context(), meta.BlobSHA256)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		defer func() { _ = reader.Close() }()

		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.Copy(w, reader)
	})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/repos/nonexistent-repo/dists/bookworm/Release", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

// debTxCountingDB fails BeginTx so a deb regeneration ends right after taking
// its transaction, which is enough to observe that it ran.
type debTxCountingDB struct {
	*MockDatabase

	beginCalls atomic.Int64
}

var errTestNoTx = errors.New("no transaction in test")

func (d *debTxCountingDB) BeginTx(context.Context) (database.Transaction, error) {
	d.beginCalls.Add(1)
	return nil, errTestNoTx
}

// A pending deb regeneration must run to completion when the worker stops, not
// be discarded, and repeated notifications must coalesce into one run.
func TestDebWorkerNotifyStaleDrainsOnStop(t *testing.T) {
	db := &debTxCountingDB{MockDatabase: newMockDatabase()}

	log, _ := logger.New("error", "json", "stdout")
	debWorker := worker.NewDebMetadataWorker(db, newMockBlobStore(), nil, log).WithDebounce(time.Hour)

	for range 5 {
		debWorker.NotifyStale("repo-1")
	}

	debWorker.Stop()

	if got := db.beginCalls.Load(); got != 1 {
		t.Errorf("deb regeneration ran %d times during Stop(), want 1", got)
	}
}
