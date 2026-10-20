// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type deltaTestDB struct {
	database.BaseMockDB

	comps []models.Component
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

func (d *deltaTestDB) GetComponentsByRepo(context.Context, string) ([]models.Component, error) {
	return d.comps, nil
}

func (d *deltaTestDB) GetPackageDelta(context.Context, string, string, string) (*models.PackageDelta, error) {
	return nil, nil
}

func (d *deltaTestDB) CreatePackageDelta(context.Context, *models.PackageDelta) error { return nil }

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
}
