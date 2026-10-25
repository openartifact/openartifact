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

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const mavenGuardPath = "/repos/maven/releases/com/acme/app/1.0.0/app-1.0.0.jar"

var errMavenGuardDB = errors.New("pq: connection refused to 10.0.0.5:5432")

// mavenGuardDB serves one hosted repository and one artifact. The repository
// lookup of the immutable-releases guard is the second one of a delete (the
// first resolves the repository id); failGuardLookup makes it fail.
type mavenGuardDB struct {
	databasetest.BaseMockDB

	immutable       bool
	failGuardLookup bool
	repoLookups     int
}

func (d *mavenGuardDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	d.repoLookups++

	if d.failGuardLookup && d.repoLookups > 1 {
		return nil, errMavenGuardDB
	}

	return &models.Repository{ID: "repo-1", Name: name, Format: "maven", RepoType: "hosted", ImmutableReleases: d.immutable}, nil
}

func (d *mavenGuardDB) GetMavenArtifactByPath(_ context.Context, _, _ string) (*models.MavenArtifact, error) {
	return &models.MavenArtifact{ID: "art-1", GroupID: "com.acme", ArtifactCoord: "app", Version: "1.0.0"}, nil
}

func deleteMaven(t *testing.T, db database.Database) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	router := chi.NewRouter()
	NewMavenUploadHandler(log, db, nil, nil).Register(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodDelete, mavenGuardPath, nil))

	return rec
}

func TestMavenDelete_ImmutableReleaseIsConflict(t *testing.T) {
	rec := deleteMaven(t, &mavenGuardDB{immutable: true})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), errMavenImmutableRelease) {
		t.Errorf("body = %s, want the static immutable-release message", rec.Body.String())
	}
}

// A failing guard lookup is a server error: it must not read as an
// immutability conflict nor leak the driver's error text.
func TestMavenDelete_GuardLookupFailureIsInternalError(t *testing.T) {
	rec := deleteMaven(t, &mavenGuardDB{immutable: true, failGuardLookup: true})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}

	if body := rec.Body.String(); strings.Contains(body, "10.0.0.5") || strings.Contains(body, "connection refused") {
		t.Errorf("body leaks the database error: %q", body)
	}

	if body := strings.TrimSpace(rec.Body.String()); body != "internal error" {
		t.Errorf("body = %q, want %q", body, "internal error")
	}
}
