// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

const secretScanID = "66666666-6666-4666-8666-666666666666"

// secretScanStoreDB keeps secret scans per repository, like the real store.
type secretScanStoreDB struct {
	databasetest.BaseMockDB

	scans map[string][]*models.SecretScanResult
}

func (db *secretScanStoreDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: testRepoID, Name: name, Format: "raw"}, nil
}

func (db *secretScanStoreDB) ListSecretScanResults(_ context.Context, repoID string, _ bool) ([]*models.SecretScanResult, error) {
	return db.scans[repoID], nil
}

func (db *secretScanStoreDB) DeleteSecretScanResult(_ context.Context, repoID, id string) error {
	for i, s := range db.scans[repoID] {
		if s.ID == id {
			db.scans[repoID] = slices.Delete(db.scans[repoID], i, i+1)
			return nil
		}
	}

	return database.ErrSecretScanNotFound
}

func TestSecretsDeleteScanRemovesIt(t *testing.T) {
	db := &secretScanStoreDB{scans: map[string][]*models.SecretScanResult{
		testRepoID: {{ID: secretScanID, ArtifactID: ".env", ScanStatus: "flagged"}},
	}}
	log, _ := newBundlesScansEnv(t, &bundlesScansMockDB{})
	h := NewSecretsHandler(db, log, nil)

	do := func(method, id string) int {
		return serveAs(func(r chi.Router) {
			h.RegisterRoutes(r, "/api/v1/repositories/{name}")
		}, method, "/api/v1/repositories/raw/secrets/scans/"+id, "").Code
	}

	if code := do(http.MethodGet, secretScanID); code != http.StatusOK {
		t.Fatalf("GET before delete = %d", code)
	}

	if code := do(http.MethodDelete, secretScanID); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}

	if code := do(http.MethodGet, secretScanID); code != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", code)
	}

	if code := do(http.MethodDelete, secretScanID); code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", code)
	}

	if code := do(http.MethodDelete, "not-a-uuid"); code != http.StatusNotFound {
		t.Fatalf("DELETE with a bad id = %d, want 404", code)
	}
}
