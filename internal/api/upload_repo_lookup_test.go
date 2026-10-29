// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
)

// lookupFailDB answers the repository lookup the way the test sets it up and
// fails package lookups by a non-uuid id like the uuid-keyed tables do. Every
// other method panics (nil embedded store): the handlers must stop before them.
type lookupFailDB struct {
	RPMUploadStore

	repo    *models.Repository
	repoErr error
}

func (d *lookupFailDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return d.repo, d.repoErr
}

func (d *lookupFailDB) GetRpmPackageByID(_ context.Context, id string) (*models.RpmPackage, error) {
	return nil, errors.New("invalid input syntax for type uuid: " + id)
}

func (d *lookupFailDB) GetDebPackageByID(_ context.Context, id string) (*models.DebPackage, error) {
	return nil, errors.New("invalid input syntax for type uuid: " + id)
}

const lookupPackageID = "6f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11"

func serveUploadRoutes(t *testing.T, db *lookupFailDB, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	log := newFixTestLogger(t)
	router := chi.NewRouter()
	NewRPMUploadHandler(log, db, store, nil).Register(router)
	NewDebUploadHandler(log, db, store, nil).Register(router)

	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(""))
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

// A failing repository lookup is a 500 and a missing repository a 404: a
// database outage must not look like a missing repository, or CI retries are
// misled.
func TestRPMAndDebUploadRepositoryLookupErrors(t *testing.T) {
	routes := []struct{ name, method, path string }{
		{"rpm upload", http.MethodPost, "/api/v1/repositories/rpms/packages"},
		{"rpm delete", http.MethodDelete, "/api/v1/repositories/rpms/packages/" + lookupPackageID},
		{"rpm comps", http.MethodPut, "/api/v1/repositories/rpms/comps"},
		{"deb delete", http.MethodDelete, "/api/v1/repositories/rpms/debs/" + lookupPackageID},
	}

	for _, rt := range routes {
		t.Run(rt.name+"/database error", func(t *testing.T) {
			rw := serveUploadRoutes(t, &lookupFailDB{repoErr: errFakeDB}, rt.method, rt.path)

			if rw.Code != http.StatusInternalServerError {
				t.Fatalf("%s %s = %d, want 500: %s", rt.method, rt.path, rw.Code, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), errFakeDB.Error()) {
				t.Errorf("response leaks the database error: %s", rw.Body.String())
			}
		})

		t.Run(rt.name+"/missing repository", func(t *testing.T) {
			rw := serveUploadRoutes(t, &lookupFailDB{}, rt.method, rt.path)

			if rw.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want 404: %s", rt.method, rt.path, rw.Code, rw.Body.String())
			}
		})
	}
}

// A package id that is not a uuid cannot name a package: 404, not the 500 the
// uuid column's syntax error would become.
func TestRPMAndDebDeleteNonUUIDPackageIDIsNotFound(t *testing.T) {
	for _, tc := range []struct{ name, format, path string }{
		{"rpm", "rpm", "/api/v1/repositories/repo/packages/not-a-uuid"},
		{"deb through the dispatcher", "deb", "/api/v1/repositories/repo/packages/0"},
		{"deb", "deb", "/api/v1/repositories/repo/debs/not-a-uuid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &lookupFailDB{repo: &models.Repository{ID: "r1", Name: "repo", Format: tc.format, RepoType: "hosted"}}

			rw := serveUploadRoutes(t, db, http.MethodDelete, tc.path)

			if rw.Code != http.StatusNotFound {
				t.Fatalf("DELETE %s = %d, want 404: %s", tc.path, rw.Code, rw.Body.String())
			}
		})
	}
}
