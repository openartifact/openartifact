// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

type staleUploadDB struct {
	database.BaseMockDB

	stale []string
}

func (d *staleUploadDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return &models.Repository{ID: "r1", Name: "rpms", Format: "rpm", RepoType: "hosted"}, nil
}

func (d *staleUploadDB) GetRPMRepo(context.Context, string) (*models.RPMRepo, error) {
	return &models.RPMRepo{RepositoryID: "r1"}, nil
}

func (d *staleUploadDB) ArtifactExists(context.Context, string, string) (bool, error) {
	return false, nil
}

func (d *staleUploadDB) RpmPackageExists(context.Context, string, string, string, string, int, string) (bool, error) {
	return false, nil
}

func (d *staleUploadDB) GetComponentByFilename(context.Context, string, string) (*models.Component, error) {
	return nil, nil
}

func (d *staleUploadDB) UpsertBlob(_ context.Context, sha256, _ string, _ int64) (string, error) {
	return sha256, nil
}

func (d *staleUploadDB) CreateRpmPackage(_ context.Context, pkg *models.Component) error {
	pkg.ID = "p1"
	return nil
}

func (d *staleUploadDB) ReserveStorageUsage(context.Context, string, int64, int) (bool, error) {
	return true, nil
}

func (d *staleUploadDB) MarkRepoStale(_ context.Context, repoID string) error {
	d.stale = append(d.stale, repoID)
	return nil
}

// Every successful upload flags the repository stale: the metadata worker skips
// repositories that are not flagged, so without it a second package never
// reaches primary.xml.
func TestRPMUploadMarksRepoStale(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile("../../pkg/rpm/testdata/weakdeps.rpm")
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	fw, err := mw.CreateFormFile("file", "weakdeps.rpm")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}

	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	db := &staleUploadDB{}
	router := chi.NewRouter()
	NewRPMUploadHandler(log, db, store, nil).Register(router)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories/rpms/packages", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("upload status %d: %s", rw.Code, rw.Body.String())
	}

	if len(db.stale) != 1 || db.stale[0] != "r1" {
		t.Errorf("MarkRepoStale calls = %v, want [r1]", db.stale)
	}
}
