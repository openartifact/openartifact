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
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

type updateCapturingDB struct {
	repoCreateMockDB

	repo    *models.Repository
	updated *models.Repository
}

func (db *updateCapturingDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	cp := *db.repo

	return &cp, nil
}

func (db *updateCapturingDB) UpdateRepository(_ context.Context, repo *models.Repository) error {
	db.updated = repo
	return nil
}

func patchProxy(t *testing.T, body string) *models.Repository {
	t.Helper()

	db := &updateCapturingDB{repo: &models.Repository{
		ID: "r1", Name: "px", Format: "maven", RepoType: "proxy",
		UpstreamURL: "https://93.184.216.34/maven", UpstreamUsername: "svc", UpstreamPasswordEnc: "stored-secret",
	}}
	db.targets = adminTargets()

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	req := adminRequest(&db.repoCreateMockDB, http.MethodPatch, "/api/v1/repositories/px", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "px")

	rw := httptest.NewRecorder()
	h.handleUpdate(rw, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))

	if rw.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rw.Code, rw.Body.String())
	}

	if db.updated == nil {
		t.Fatal("repository was not updated")
	}

	return db.updated
}

func TestPatchUpstreamDropsStoredPasswordWhenRetargeted(t *testing.T) {
	cases := []struct {
		name, body string
		wantKept   bool
	}{
		{"new host", `{"upstream_url":"https://93.184.216.35/maven"}`, false},
		{"new scheme", `{"upstream_url":"http://93.184.216.34/maven"}`, false},
		{"new port", `{"upstream_url":"https://93.184.216.34:8443/maven"}`, false},
		{"new username", `{"upstream_username":"other"}`, false},
		{"new host, password re-entered", `{"upstream_url":"https://93.184.216.35/maven","upstream_password":"fresh"}`, true},
		{"same origin, other path", `{"upstream_url":"https://93.184.216.34:443/other"}`, true},
		{"same username", `{"upstream_username":"svc","cache_ttl_seconds":60}`, true},
		{"unrelated field", `{"description":"x"}`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := patchProxy(t, c.body).UpstreamPasswordEnc

			if c.wantKept && got == "" {
				t.Fatal("password was dropped")
			}

			if !c.wantKept && got != "" {
				t.Fatalf("stored password survived a retarget: %q", got)
			}

			if c.name == "new host, password re-entered" && got == "stored-secret" {
				t.Fatal("stored password was not replaced by the re-entered one")
			}
		})
	}
}

func patchGroupRepo(t *testing.T, repo *models.Repository, body string) (*httptest.ResponseRecorder, *models.Repository) {
	t.Helper()

	db := &updateCapturingDB{repo: repo}
	db.targets = adminTargets()

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	req := adminRequest(&db.repoCreateMockDB, http.MethodPatch, "/api/v1/repositories/g", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", repo.Name)

	rw := httptest.NewRecorder()
	h.handleUpdate(rw, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))

	return rw, db.updated
}

func TestPatchMergeProxyVersions(t *testing.T) {
	rw, updated := patchGroupRepo(t, &models.Repository{ID: "g1", Name: "g", Format: "npm", RepoType: "group"}, `{"merge_proxy_versions":true}`)
	if rw.Code != http.StatusOK || updated == nil || !updated.MergeProxyVersions {
		t.Fatalf("group: status %d updated %+v body %s", rw.Code, updated, rw.Body.String())
	}

	if !strings.Contains(rw.Body.String(), `"merge_proxy_versions":true`) {
		t.Errorf("response lacks merge_proxy_versions: %s", rw.Body.String())
	}

	rw, updated = patchGroupRepo(t, &models.Repository{ID: "g2", Name: "g", Format: "npm", RepoType: "group", MergeProxyVersions: true}, `{"merge_proxy_versions":false}`)
	if rw.Code != http.StatusOK || updated == nil || updated.MergeProxyVersions {
		t.Errorf("clearing: status %d updated %+v", rw.Code, updated)
	}

	rw, updated = patchGroupRepo(t, &models.Repository{ID: "p1", Name: "g", Format: "npm", RepoType: "proxy", UpstreamURL: "https://93.184.216.34/"}, `{"merge_proxy_versions":true}`)
	if rw.Code != http.StatusBadRequest || updated != nil {
		t.Errorf("proxy: status %d updated %+v, want 400 and no update", rw.Code, updated)
	}
}

func TestCreateMergeProxyVersions(t *testing.T) {
	db := newProxyGroupDB()
	if rw := createRepo(t, db, `{"name":"g","format":"maven","repo_type":"group","merge_proxy_versions":true}`); rw.Code != http.StatusCreated || db.created == nil || !db.created.MergeProxyVersions {
		t.Fatalf("group create: %d %s", rw.Code, rw.Body.String())
	}

	db = newProxyGroupDB()
	if rw := createRepo(t, db, `{"name":"h","format":"maven","merge_proxy_versions":true}`); rw.Code != http.StatusBadRequest || db.created != nil {
		t.Errorf("hosted create: %d %s", rw.Code, rw.Body.String())
	}

	db = newProxyGroupDB()
	if rw := createRepo(t, db, `{"name":"g2","format":"maven","repo_type":"group"}`); rw.Code != http.StatusCreated || db.created.MergeProxyVersions {
		t.Errorf("default must be hosted-wins: %d %s", rw.Code, rw.Body.String())
	}
}
