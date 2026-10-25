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

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

type mavenLimitsDB struct {
	databasetest.BaseMockDB

	quota *models.StorageQuota
}

func (d *mavenLimitsDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: "repo-1", Name: name, Format: "maven", RepoType: "hosted"}, nil
}

func (d *mavenLimitsDB) GetOrCreateStorageQuota(context.Context, string) (*models.StorageQuota, error) {
	return d.quota, nil
}

func putMaven(t *testing.T, db database.Database, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	NewMavenUploadHandler(log, db, nil, nil).Register(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPut, path, strings.NewReader(body)))

	return rec
}

func TestMavenPut_RejectsAliasingPathSegments(t *testing.T) {
	db := &mavenLimitsDB{quota: &models.StorageQuota{}}

	for _, p := range []string{
		"/repos/maven/r/com//acme/app/1.0/app-1.0.jar",
		"/repos/maven/r/com/./acme/app/1.0/app-1.0.jar",
	} {
		if rec := putMaven(t, db, p, "x"); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", p, rec.Code)
		}
	}
}

func TestMavenPut_OversizedPomIsRejected(t *testing.T) {
	db := &mavenLimitsDB{quota: &models.StorageQuota{}}
	body := "<project>" + strings.Repeat("a", pkgmaven.MaxPOMBytes) + "</project>"

	rec := putMaven(t, db, "/repos/maven/r/com/acme/app/1.0/app-1.0.pom", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized pom = %d, want 413", rec.Code)
	}
}

// A declared Content-Length that cannot fit the quota is refused before the
// body is spooled to disk.
func TestMavenPut_QuotaPrecheckBeforeSpool(t *testing.T) {
	db := &mavenLimitsDB{quota: &models.StorageQuota{MaxSizeBytes: 10, CurrentSizeBytes: 5}}

	rec := putMaven(t, db, "/repos/maven/r/com/acme/app/1.0/app-1.0.jar", strings.Repeat("a", 64))
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("over-quota upload = %d, want 507", rec.Code)
	}
}
