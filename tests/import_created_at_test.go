// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/middleware"
)

// adminOnlyManage grants "manage" to the principal named "admin".
type adminOnlyManage struct{}

func (adminOnlyManage) Evaluate(_ context.Context, p auth.Principal, _, _, action string) (bool, error) {
	return action == "manage" && p.Username == "admin", nil
}

// withImportAuth mounts register behind the same middleware chain the server
// uses (authentication result in context, then importmeta).
func withImportAuth(username string, register func(chi.Router)) chi.Router {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: username})))
		})
	})
	r.Use(importmeta.Middleware(adminOnlyManage{}))
	register(r)

	return r
}

// TestImportCreatedAt_MavenAndRPM verifies end to end through the upload
// handlers that X-OA-Import-Created-At is honored for administrators and
// ignored for everyone else.
func TestImportCreatedAt_MavenAndRPM(t *testing.T) {
	origin := time.Date(2018, 7, 8, 9, 10, 11, 0, time.UTC)
	before := time.Now().Add(-time.Minute)

	tests := []struct {
		name     string
		user     string
		imported bool
	}{
		{"admin", "admin", true},
		{"regular user", "alice", false},
	}

	for _, tc := range tests {
		t.Run(tc.name+"/maven", func(t *testing.T) {
			db := newMockDatabase()
			db.repositories["repo-1"] = &models.Repository{ID: "repo-1", Name: "maven-repo"}

			router := withImportAuth(tc.user, newMavenUploadHandlerFor(t, db).Register)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/repos/maven/maven-repo/com/example/app/1.0/app-1.0.jar", bytes.NewReader([]byte("jar")))
			req.Header.Set(importmeta.HeaderCreatedAt, origin.Format(time.RFC3339))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}

			if id := w.Header().Get("X-Oa-Artifact-Id"); id == "" {
				t.Error("missing X-OA-Artifact-Id header")
			}

			assertCreatedAt(t, mavenCreatedAt(t, db), origin, before, tc.imported)
		})

		t.Run(tc.name+"/rpm", func(t *testing.T) {
			db := newMockDatabase()
			seedTestRepo(db, "repo-rpm", "rpmrepo")

			router := withImportAuth(tc.user, api.NewRPMUploadHandler(newHandlerLogger(t), db, newMockBlobStore(), nil).Register)

			req := buildRPMUploadRequest(t, buildMinimalRPM(), "rpmrepo")
			req.Header.Set(importmeta.HeaderCreatedAt, origin.Format(time.RFC3339))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}

			var created time.Time

			for _, c := range db.components {
				created = c.CreatedAt
			}

			assertCreatedAt(t, created, origin, before, tc.imported)
		})
	}
}

func newMavenUploadHandlerFor(t *testing.T, db *MockDatabase) *api.MavenUploadHandler {
	t.Helper()

	return api.NewMavenUploadHandler(newHandlerLogger(t), db, newMockBlobStore(), nil)
}

func mavenCreatedAt(t *testing.T, db *MockDatabase) time.Time {
	t.Helper()

	for _, a := range db.mavenArtifacts {
		return a.CreatedAt
	}

	t.Fatal("no maven artifact stored")

	return time.Time{}
}

func assertCreatedAt(t *testing.T, got, origin, before time.Time, imported bool) {
	t.Helper()

	switch {
	case imported && !got.Equal(origin):
		t.Errorf("created_at = %v, want origin %v", got, origin)
	case !imported && !got.IsZero() && got.Before(before):
		t.Errorf("created_at = %v: non-admin header was honored", got)
	case !imported && got.Equal(origin):
		t.Errorf("created_at = %v: non-admin header was honored", got)
	}
}
