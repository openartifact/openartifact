// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// mavenDeployDB answers the repository lookups of a maven PUT with a group and
// its hosted member, then fails the lookup it is asked for, so the test sees
// which repository the handler tried to publish to without a full store.
type mavenDeployDB struct {
	deleteMavenStore

	looked []string
}

func (d *mavenDeployDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	d.looked = append(d.looked, name)

	if name == "grp" {
		return &models.Repository{ID: "g", Name: name, Format: "maven", RepoType: "group"}, nil
	}

	return nil, errMavenGuardDB
}

func putMavenDeploy(t *testing.T, db *mavenDeployDB, ctx context.Context) int {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	router := chi.NewRouter()
	NewMavenUploadHandler(log, db, nil, nil).Register(router)

	req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/repos/maven/grp/com/acme/app/1.0/app-1.0.jar", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec.Code
}

func TestMavenPut_GroupDeployTarget(t *testing.T) {
	t.Run("a group without an authorized target is read-only", func(t *testing.T) {
		db := &mavenDeployDB{}
		if code := putMavenDeploy(t, db, t.Context()); code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", code)
		}
	})

	t.Run("the authorized target is the repository written", func(t *testing.T) {
		db := &mavenDeployDB{}

		ctx := middleware.WithGroupDeploy(t.Context(), "grp", "tgt")
		if code := putMavenDeploy(t, db, ctx); code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 from the failing target lookup", code)
		}

		if len(db.looked) != 1 || db.looked[0] != "tgt" {
			t.Fatalf("looked up %v, want only the target", db.looked)
		}
	})
}
