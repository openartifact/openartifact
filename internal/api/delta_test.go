// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type deltaTestDB struct {
	database.BaseMockDB

	comps       []models.Component
	quarantined map[string]bool
	upserted    []string
}

func (d *deltaTestDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: "repo1", Name: name}, nil
}

func (d *deltaTestDB) GetComponentByID(_ context.Context, id string) (*models.Component, error) {
	for i := range d.comps {
		if d.comps[i].ID == id {
			return &d.comps[i], nil
		}
	}

	return nil, nil
}

func (d *deltaTestDB) GetComponentBySHA256(_ context.Context, repoID, sha string) (*models.Component, error) {
	for i := range d.comps {
		if d.comps[i].RepositoryID == repoID && d.comps[i].SHA256 == sha {
			return &d.comps[i], nil
		}
	}

	return nil, nil
}

func (d *deltaTestDB) GetPackageDelta(context.Context, string, string, string) (*models.PackageDelta, error) {
	return nil, nil
}

func (d *deltaTestDB) CreatePackageDelta(context.Context, *models.PackageDelta) error { return nil }

func (d *deltaTestDB) UpsertBlob(_ context.Context, sha, _ string, _ int64) (string, error) {
	d.upserted = append(d.upserted, sha)
	return "blob-" + sha, nil
}

// IsArtifactQuarantined makes deltaTestDB a quarantine checker.
func (d *deltaTestDB) IsArtifactQuarantined(_ context.Context, _ string, keys []string) (bool, error) {
	for _, k := range keys {
		if d.quarantined[k] {
			return true, nil
		}
	}

	return false, nil
}

func TestDeltaFromValidationAndScoping(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	oldID, _ := store.Put(ctx, bytes.NewReader([]byte("old package content")), nil)
	newID, _ := store.Put(ctx, bytes.NewReader([]byte("new package content!")), nil)

	db := &deltaTestDB{comps: []models.Component{
		{ID: "to", RepositoryID: "repo1", SHA256: newID, Filename: "new.pkg"},
		{ID: "from", RepositoryID: "repo1", SHA256: oldID, Filename: "old.pkg"},
		{ID: "foreign", RepositoryID: "other", SHA256: newID, Filename: "x.pkg"},
	}}

	router := chi.NewRouter()
	NewDeltaHandler(log, db, store).Register(router)

	get := func(path string) int {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody))

		return rec.Code
	}

	base := "/api/v1/repositories/r/packages/"

	if c := get(base + "to/delta?from=../../../../etc/passwd"); c != http.StatusBadRequest {
		t.Errorf("traversal from: got %d, want 400", c)
	}

	unknown := "00000000000000000000000000000000000000000000000000000000000000aa"
	if c := get(base + "to/delta?from=" + unknown); c != http.StatusNotFound {
		t.Errorf("from not in repo: got %d, want 404", c)
	}

	if c := get(base + "foreign/delta?from=" + oldID); c != http.StatusNotFound {
		t.Errorf("foreign-repo target: got %d, want 404", c)
	}

	if c := get(base + "to/delta?from=" + oldID); c != http.StatusOK {
		t.Errorf("valid delta: got %d, want 200", c)
	}

	// API-1: the endpoint is RBAC-gated, so a delta response must never be
	// marked public for a request that is not an anonymous public read.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, base+"to/delta?from="+oldID, http.NoBody))

	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "public") {
		t.Errorf("Cache-Control = %q, must not be public", cc)
	}
}

func newDeltaFixture(t *testing.T) (*chi.Mux, *deltaTestDB, *DeltaHandler, string) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	oldID, _ := store.Put(context.Background(), bytes.NewReader([]byte("old package content")), nil)
	newID, _ := store.Put(context.Background(), bytes.NewReader([]byte("new package content!")), nil)

	db := &deltaTestDB{comps: []models.Component{
		{ID: "to", RepositoryID: "repo1", SHA256: newID, Filename: "new.pkg"},
		{ID: "from", RepositoryID: "repo1", SHA256: oldID, Filename: "old.pkg"},
	}, quarantined: map[string]bool{}}

	h := NewDeltaHandler(log, db, store)
	router := chi.NewRouter()
	h.Register(router)

	return router, db, h, oldID
}

func TestDeltaDeniesQuarantinedSides(t *testing.T) {
	for _, key := range []string{"new.pkg", "old.pkg"} {
		router, db, _, oldID := newDeltaFixture(t)
		db.quarantined[key] = true

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet,
			"/api/v1/repositories/r/packages/to/delta?from="+oldID, http.NoBody))

		if rec.Code != http.StatusForbidden {
			t.Errorf("quarantined %s: got %d, want 403", key, rec.Code)
		}

		if len(db.upserted) != 0 {
			t.Errorf("quarantined %s: a patch was generated", key)
		}
	}
}

func TestDeltaGenerationRegistersBlob(t *testing.T) {
	router, db, _, oldID := newDeltaFixture(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v1/repositories/r/packages/to/delta?from="+oldID, http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	if len(db.upserted) != 1 {
		t.Errorf("patch blob registrations = %d, want 1 (so GC and refcounts see it)", len(db.upserted))
	}
}

func TestDeltaGenerationIsBounded(t *testing.T) {
	_, db, h, oldID := newDeltaFixture(t)

	for range maxConcurrentDeltas {
		h.slots <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", http.NoBody)

	if _, err := h.generateDelta(req, "repo1", oldID, "old.pkg", &db.comps[0]); err == nil {
		t.Fatal("generation ran although every slot was taken")
	}
}
