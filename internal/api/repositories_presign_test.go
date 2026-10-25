// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

type repoPresignDB struct {
	databasetest.BaseMockDB

	repo    *models.Repository
	targets []*models.PermissionTarget
	updated int
}

func (d *repoPresignDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	cp := *d.repo
	return &cp, nil
}

func (d *repoPresignDB) UpdateRepository(_ context.Context, r *models.Repository) error {
	d.repo = r
	d.updated++

	return nil
}

func (d *repoPresignDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return d.targets, nil
}

func patchRepo(t *testing.T, db *repoPresignDB, body string) (int, map[string]any) {
	t.Helper()

	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	r := chi.NewRouter()
	r.Patch("/api/v1/repository/{name}", h.handleUpdate)

	req := adminRequest(nil, http.MethodPatch, "/api/v1/repository/files", body)
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	var out map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	return rw.Code, out
}

func TestRepositoryPresignOverride_TriState(t *testing.T) {
	db := &repoPresignDB{repo: &models.Repository{ID: "r1", Name: "files", Format: "raw", RepoType: "hosted"}, targets: adminTargets()}

	// A PATCH that does not mention the field leaves it inherited and, in the
	// response, null.
	code, out := patchRepo(t, db, `{"description":"x"}`)
	if code != http.StatusOK || out["presign_downloads"] != nil || db.repo.PresignDownloads != nil {
		t.Fatalf("untouched: status %d, response %v, stored %v", code, out["presign_downloads"], db.repo.PresignDownloads)
	}

	for _, tc := range []struct {
		body string
		want bool
	}{{`{"presign_downloads":true}`, true}, {`{"presign_downloads":false}`, false}} {
		code, out = patchRepo(t, db, tc.body)
		if code != http.StatusOK || db.repo.PresignDownloads == nil || *db.repo.PresignDownloads != tc.want || out["presign_downloads"] != tc.want {
			t.Fatalf("%s: status %d, response %v, stored %v", tc.body, code, out["presign_downloads"], db.repo.PresignDownloads)
		}
	}

	// Absent leaves an explicit override alone.
	if code, _ = patchRepo(t, db, `{"description":"y"}`); code != http.StatusOK || db.repo.PresignDownloads == nil || *db.repo.PresignDownloads {
		t.Fatalf("an unrelated PATCH changed the override: %v", db.repo.PresignDownloads)
	}

	// Explicit null returns to inheriting the global setting.
	code, out = patchRepo(t, db, `{"presign_downloads":null}`)
	if code != http.StatusOK || db.repo.PresignDownloads != nil {
		t.Fatalf("null: status %d, stored %v", code, db.repo.PresignDownloads)
	}

	if v, present := out["presign_downloads"]; !present || v != nil {
		t.Errorf("response presign_downloads = %v (present=%v), want null", v, present)
	}
}

func TestRepositoryPresignOverride_RejectsNonBoolean(t *testing.T) {
	db := &repoPresignDB{repo: &models.Repository{ID: "r1", Name: "files", Format: "raw", RepoType: "hosted"}, targets: adminTargets()}

	for _, body := range []string{`{"presign_downloads":"yes"}`, `{"presign_downloads":1}`, `{"presign_downloads":{}}`} {
		if code, _ := patchRepo(t, db, body); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, code)
		}
	}

	if db.updated != 0 {
		t.Errorf("an invalid override reached the database (%d updates)", db.updated)
	}
}

func TestRepositoryPresignOverride_CreateAcceptsIt(t *testing.T) {
	log := newFixTestLogger(t)
	db := &repoCreateMockDB{targets: adminTargets()}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"f","format":"raw","presign_downloads":false}`))

	if rw.Code != http.StatusCreated || db.created == nil || db.created.PresignDownloads == nil || *db.created.PresignDownloads {
		t.Fatalf("status %d, created %+v; body %s", rw.Code, db.created, rw.Body.String())
	}

	if !strings.Contains(rw.Body.String(), `"presign_downloads":false`) {
		t.Errorf("response does not report the override: %s", rw.Body.String())
	}
}
