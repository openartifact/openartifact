// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type compsDB struct {
	databasetest.BaseMockDB

	meta map[string]*models.RepoMetadata
}

func (d *compsDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return &models.Repository{ID: "r1", Name: "rpms", Format: "rpm", RepoType: "hosted"}, nil
}

func (d *compsDB) GetRPMRepo(context.Context, string) (*models.RPMRepo, error) {
	return &models.RPMRepo{RepositoryID: "r1"}, nil
}

func (d *compsDB) GetRepoMetadataByPath(_ context.Context, _, path string) (*models.RepoMetadata, error) {
	return d.meta[path], nil
}

func (d *compsDB) UpsertRepoMetadata(_ context.Context, m *models.RepoMetadata) error {
	d.meta[m.Path] = m
	return nil
}

func (d *compsDB) MarkRepoStale(context.Context, string) error { return nil }

// Replacing comps.xml stores the new blob, repoints the single metadata row and
// leaves the superseded blob to the reclaim path (the row no longer references it).
func TestCompsUploadReplacesRow(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	db := &compsDB{meta: map[string]*models.RepoMetadata{}}
	h := NewRPMUploadHandler(log, db, store, nil)
	router := chi.NewRouter()
	h.Register(router)

	put := func(body string) {
		t.Helper()

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/api/v1/repositories/rpms/comps", strings.NewReader(body)))

		if rw.Code >= 300 {
			t.Fatalf("status %d: %s", rw.Code, rw.Body.String())
		}
	}

	put(`<comps><group><id>a</id></group></comps>`)

	first := db.meta["comps.xml"]
	if first == nil {
		t.Fatal("comps row not stored")
	}

	put(`<comps><group><id>b</id></group></comps>`)

	second := db.meta["comps.xml"]
	if second == nil || second.BlobSHA256 == first.BlobSHA256 {
		t.Fatalf("row not repointed: %+v", second)
	}

	if _, ok, _ := store.ExistsBySHA256(context.Background(), second.BlobSHA256); !ok {
		t.Error("current comps blob missing")
	}
}
