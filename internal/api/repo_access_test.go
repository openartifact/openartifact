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
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// repoAccessDBStore is what repoAccessDB is used as.
type repoAccessDBStore interface {
	repoAccessStore
	auth.EvaluatorStore
}

type repoAccessDB struct {
	repoAccessDBStore
}

// ListProjectGrantsByUser is not exercised by the tests.
func (*repoAccessDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

func (*repoAccessDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if name == "known" {
		return &models.Repository{ID: "11111111-1111-4111-8111-111111111111", Name: name}, nil
	}

	return nil, nil
}

func (*repoAccessDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return nil, nil
}

// TestRepoAccessGateOrder pins what each prologue answers for an unknown or a
// known repository: routes differ on whether a caller that is not allowed
// learns about the repository's existence (404 before 403) or not.
func TestRepoAccessGateOrder(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	db := &repoAccessDB{}
	access := newRepoAccess(db, log, auth.NewEvaluator(db, log, false))
	userID := "22222222-2222-4222-8222-222222222222"

	variants := map[string]func(http.ResponseWriter, *http.Request) (*models.Repository, bool){
		"adminRepo": func(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
			return access.adminRepo(w, r, "op")
		},
		"adminFirstRepo": func(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
			return access.adminFirstRepo(w, r, "op")
		},
		"repoThenRepoAdmin": func(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
			return access.repoThenRepoAdmin(w, r, "op")
		},
		"repoAdminFirst": func(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
			return access.repoAdminFirst(w, r, "op")
		},
		"permittedRepo": func(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
			return access.permittedRepo(w, r, "op", "delete", false)
		},
	}

	tests := []struct {
		variant   string
		repo      string
		principal *auth.Principal
		want      int
	}{
		{"adminRepo", "ghost", &auth.Principal{UserID: &userID}, http.StatusNotFound},
		{"adminRepo", "known", &auth.Principal{UserID: &userID}, http.StatusForbidden},
		{"adminRepo", "known", &auth.Principal{IsLegacy: true}, http.StatusOK},
		{"adminFirstRepo", "ghost", &auth.Principal{UserID: &userID}, http.StatusForbidden},
		{"adminFirstRepo", "ghost", &auth.Principal{IsLegacy: true}, http.StatusNotFound},
		{"repoThenRepoAdmin", "ghost", &auth.Principal{UserID: &userID}, http.StatusNotFound},
		{"repoThenRepoAdmin", "known", &auth.Principal{IsAnonymous: true}, http.StatusForbidden},
		{"repoAdminFirst", "ghost", &auth.Principal{UserID: &userID}, http.StatusForbidden},
		{"repoAdminFirst", "ghost", &auth.Principal{IsLegacy: true}, http.StatusNotFound},
		{"permittedRepo", "ghost", &auth.Principal{IsAnonymous: true}, http.StatusForbidden},
		{"permittedRepo", "ghost", &auth.Principal{IsLegacy: true}, http.StatusNotFound},
		{"permittedRepo", "known", &auth.Principal{IsLegacy: true}, http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.variant+"/"+tc.repo, func(t *testing.T) {
			router := chi.NewRouter()
			router.Get("/repos/{name}", func(w http.ResponseWriter, r *http.Request) {
				if _, ok := variants[tc.variant](w, r); ok {
					w.WriteHeader(http.StatusOK)
				}
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/"+tc.repo, http.NoBody)
			req = req.WithContext(middleware.WithPrincipal(req.Context(), tc.principal))

			rw := httptest.NewRecorder()
			router.ServeHTTP(rw, req)

			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d, body %s", rw.Code, tc.want, rw.Body.String())
			}
		})
	}
}

func TestMapListNeverNil(t *testing.T) {
	got := mapList([]int(nil), func(i int) int { return i })
	if got == nil || len(got) != 0 {
		t.Fatalf("mapList(nil) = %#v, want empty non-nil slice", got)
	}
}
