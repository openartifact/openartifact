// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func setupUnpublishScenario(t *testing.T, allowFull bool, versionAge time.Duration) (*MockDatabase, *MockBlobStore) {
	t.Helper()

	db := newMockDatabase()
	bs := newMockBlobStore()

	db.repositories["repo-1"] = &models.Repository{
		ID: "repo-1", Name: "myrepo", AllowFullUnpublish: allowFull,
	}
	db.npmPackages["pkg-1"] = &models.NPMPackage{
		ID: "pkg-1", RepositoryID: "repo-1", FullName: "lodash", Name: "lodash",
	}
	db.npmVersions["pkg-1:1.0.0"] = &models.NPMVersion{
		ID:        "v1",
		PackageID: "pkg-1",
		Version:   "1.0.0",
		CreatedAt: time.Now().Add(-versionAge),
	}

	return db, bs
}

// npm policy: a version may be unpublished freely within 72h of publication;
// after that only repositories with AllowFullUnpublish may remove it.
func TestNPMUnpublish_Under72h_200(t *testing.T) {
	db, bs := setupUnpublishScenario(t, false, 1*time.Hour)
	router := setupUploadRouter(db, bs)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/repos/myrepo/lodash/-rev/1-abc/1.0.0", nil)
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNPMUnpublish_Under72h_WithOverride_200(t *testing.T) {
	db, bs := setupUnpublishScenario(t, true, 1*time.Hour)
	router := setupUploadRouter(db, bs)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/repos/myrepo/lodash/-rev/1-abc/1.0.0", nil)
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNPMUnpublish_Over72h_Blocks_403(t *testing.T) {
	db, bs := setupUnpublishScenario(t, false, 73*time.Hour)
	router := setupUploadRouter(db, bs)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/repos/myrepo/lodash/-rev/1-abc/1.0.0", nil)
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestNPMUnpublish_Over72h_WithOverride_200(t *testing.T) {
	db, bs := setupUnpublishScenario(t, true, 73*time.Hour)
	router := setupUploadRouter(db, bs)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/repos/myrepo/lodash/-rev/1-abc/1.0.0", nil)
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestNPMUnpublish_SingleVersion(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo", AllowFullUnpublish: true}
	db.npmPackages["pkg-1"] = &models.NPMPackage{ID: "pkg-1", RepositoryID: "repo-1", FullName: "lodash"}
	db.npmVersions["pkg-1:1.0.0"] = &models.NPMVersion{
		ID: "v1", PackageID: "pkg-1", Version: "1.0.0", TarballFilename: "lodash-1.0.0.tgz",
		CreatedAt: time.Now().Add(-73 * time.Hour),
	}
	db.npmVersions["pkg-1:2.0.0"] = &models.NPMVersion{
		ID: "v2", PackageID: "pkg-1", Version: "2.0.0", TarballFilename: "lodash-2.0.0.tgz",
		CreatedAt: time.Now().Add(-73 * time.Hour),
	}

	router := setupUploadRouter(db, bs)

	// `npm unpublish lodash@1.0.0` ends with a DELETE of that version's tarball.
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodDelete,
		"/repos/myrepo/lodash/-/lodash-1.0.0.tgz/-rev/1-abc",
		nil,
	)
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	if _, exists := db.npmVersions["pkg-1:1.0.0"]; exists {
		t.Error("version 1.0.0 should be deleted")
	}

	if _, exists := db.npmVersions["pkg-1:2.0.0"]; !exists {
		t.Error("version 2.0.0 should remain")
	}

	if _, exists := db.npmPackages["pkg-1"]; !exists {
		t.Error("package should remain after single version delete")
	}
}

func TestNPMUnpublish_WholePackage(t *testing.T) {
	db := newMockDatabase()
	bs := newMockBlobStore()
	db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "myrepo", AllowFullUnpublish: true}
	db.npmPackages["pkg-1"] = &models.NPMPackage{ID: "pkg-1", RepositoryID: "repo-1", FullName: "lodash"}
	db.npmVersions["pkg-1:1.0.0"] = &models.NPMVersion{
		ID: "v1", PackageID: "pkg-1", Version: "1.0.0",
		CreatedAt: time.Now().Add(-73 * time.Hour),
	}
	db.npmVersions["pkg-1:2.0.0"] = &models.NPMVersion{
		ID: "v2", PackageID: "pkg-1", Version: "2.0.0",
		CreatedAt: time.Now().Add(-73 * time.Hour),
	}
	db.npmDistTags["pkg-1"] = map[string]string{"latest": "2.0.0"}

	router := setupUploadRouter(db, bs)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/repos/myrepo/lodash", nil)
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	if _, exists := db.npmPackages["pkg-1"]; exists {
		t.Error("package should be deleted")
	}

	if _, exists := db.npmVersions["pkg-1:1.0.0"]; exists {
		t.Error("version 1.0.0 should be deleted")
	}

	if _, exists := db.npmVersions["pkg-1:2.0.0"]; exists {
		t.Error("version 2.0.0 should be deleted")
	}
}
