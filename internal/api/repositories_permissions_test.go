// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// TestRepositoryGetReportsPermissions: the SPA gates the trash tab (delete) and
// "Empty trash" (manage) on these flags, so they must follow the evaluator.
func TestRepositoryGetReportsPermissions(t *testing.T) {
	type perms struct{ Write, Delete, Manage bool }

	for _, tc := range []struct {
		name    string
		actions []string
		want    perms
	}{
		{"admin", []string{"manage", "read", "write", "delete"}, perms{Write: true, Delete: true, Manage: true}},
		{"reader", []string{"read"}, perms{}},
		{"deleter", []string{"read", "delete"}, perms{Delete: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &repoPresignDB{
				repo:    &models.Repository{ID: "r1", Name: "files", Format: "raw", RepoType: "hosted"},
				targets: []*models.PermissionTarget{{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: tc.actions}},
			}
			log := newFixTestLogger(t)
			h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

			r := chi.NewRouter()
			r.Get("/api/v1/repository/{name}", h.handleGet)

			rw := httptest.NewRecorder()
			r.ServeHTTP(rw, adminRequest(nil, http.MethodGet, "/api/v1/repository/files", ""))

			if rw.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rw.Code, rw.Body.String())
			}

			var out struct {
				Permissions *perms `json:"permissions"`
			}
			if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil || out.Permissions == nil {
				t.Fatalf("no permissions in response (%v): %s", err, rw.Body.String())
			}

			if *out.Permissions != tc.want {
				t.Fatalf("permissions = %+v, want %+v", *out.Permissions, tc.want)
			}
		})
	}
}
