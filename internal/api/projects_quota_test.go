// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/worker"
)

// quotaMockDB adds the project quota and project cleanup policy store to the
// projects fixture.
type quotaMockDB struct {
	*projectsMockDB

	quota    *models.ProjectQuota
	usage    *models.ProjectUsage
	policies map[string]*models.CleanupPolicy
	nextID   int
}

func newQuotaMock() *quotaMockDB {
	return &quotaMockDB{
		projectsMockDB: newProjectsMock(),
		policies:       map[string]*models.CleanupPolicy{},
	}
}

func (db *quotaMockDB) GetProjectQuota(_ context.Context, projectID string) (*models.ProjectQuota, error) {
	if db.quota != nil {
		cp := *db.quota
		return &cp, nil
	}

	return &models.ProjectQuota{ProjectID: projectID, WarnThresholdPercent: 80}, nil
}

func (db *quotaMockDB) SetProjectQuota(_ context.Context, projectID string, size, count int64, warn int) (*models.ProjectQuota, error) {
	db.quota = &models.ProjectQuota{ProjectID: projectID, MaxSizeBytes: size, MaxArtifactCount: count, WarnThresholdPercent: warn}

	return db.quota, nil
}

func (db *quotaMockDB) GetProjectUsage(_ context.Context, _ string) (*models.ProjectUsage, error) {
	if db.usage != nil {
		return db.usage, nil
	}

	return &models.ProjectUsage{}, nil
}

func (db *quotaMockDB) CreateCleanupPolicy(_ context.Context, p *models.CleanupPolicy) error {
	db.nextID++
	p.ID = "00000000-0000-4000-8000-00000000c0" + string(rune('0'+db.nextID))
	db.policies[p.ID] = p

	return nil
}

func (db *quotaMockDB) GetCleanupPolicy(_ context.Context, id string) (*models.CleanupPolicy, error) {
	return db.policies[id], nil
}

func (db *quotaMockDB) UpdateCleanupPolicy(_ context.Context, p *models.CleanupPolicy) error {
	db.policies[p.ID] = p
	return nil
}

func (db *quotaMockDB) DeleteCleanupPolicy(_ context.Context, id string) error {
	delete(db.policies, id)
	return nil
}

func (db *quotaMockDB) ListProjectCleanupPolicies(_ context.Context, projectID string) ([]*models.CleanupPolicy, error) {
	var out []*models.CleanupPolicy

	for _, p := range db.policies {
		if p.ProjectID == projectID {
			out = append(out, p)
		}
	}

	return out, nil
}

func newQuotaRouter(t *testing.T, db *quotaMockDB) http.Handler {
	t.Helper()

	log := newFixTestLogger(t)

	router := chi.NewRouter()
	NewProjectsHandler(db, log, auth.NewEvaluator(db, log, false), auth.NewProjectSettings(db, false)).RegisterRoutes(router)

	return router
}

func TestProjectQuota_PutIsAdminOnly(t *testing.T) {
	tests := []struct {
		name   string
		user   string
		body   string
		status int
		want   int64
	}{
		{"global admin sets the ceiling", uAdmin1, `{"max_size_bytes":1000,"max_artifact_count":5}`, http.StatusOK, 1000},
		{"owner cannot raise their own ceiling", uOwner1, `{"max_size_bytes":1000}`, http.StatusForbidden, 0},
		{"maintainer is refused", uMaint1, `{"max_size_bytes":1000}`, http.StatusForbidden, 0},
		{"viewer is refused", uView1, `{"max_size_bytes":1000}`, http.StatusForbidden, 0},
		{"non member does not learn the project exists", uNewbie, `{"max_size_bytes":1000}`, http.StatusNotFound, 0},
		{"negative limit", uAdmin1, `{"max_size_bytes":-1}`, http.StatusBadRequest, 0},
		{"threshold out of range", uAdmin1, `{"warn_threshold_percent":101}`, http.StatusBadRequest, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newQuotaMock()
			router := newQuotaRouter(t, db)

			code, body := projectJSON(t, router, tt.user, http.MethodPut, "/api/v1/projects/acme/quota", tt.body)
			if code != tt.status {
				t.Fatalf("status = %d, want %d; body = %v", code, tt.status, body)
			}

			var got int64
			if db.quota != nil {
				got = db.quota.MaxSizeBytes
			}

			if got != tt.want {
				t.Fatalf("stored max_size_bytes = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestProjectQuota_PutKeepsOmittedFields(t *testing.T) {
	db := newQuotaMock()
	db.quota = &models.ProjectQuota{MaxSizeBytes: 500, MaxArtifactCount: 9, WarnThresholdPercent: 60}
	router := newQuotaRouter(t, db)

	if code, body := projectJSON(t, router, uAdmin1, http.MethodPut, "/api/v1/projects/acme/quota", `{"max_size_bytes":700}`); code != http.StatusOK {
		t.Fatalf("status = %d; body = %v", code, body)
	}

	if q := db.quota; q.MaxSizeBytes != 700 || q.MaxArtifactCount != 9 || q.WarnThresholdPercent != 60 {
		t.Fatalf("quota = %+v, want 700 / 9 / 60", q)
	}
}

func TestProjectUsage_ReadableByEveryMember(t *testing.T) {
	tests := []struct {
		user   string
		status int
	}{
		{uView1, http.StatusOK},
		{uDev1, http.StatusOK},
		{uOwner1, http.StatusOK},
		{uAdmin1, http.StatusOK},
		{uNewbie, http.StatusNotFound},
	}

	for _, path := range []string{"usage", "quota", "cleanup-policies"} {
		for _, tt := range tests {
			db := newQuotaMock()
			router := newQuotaRouter(t, db)

			if code, body := projectJSON(t, router, tt.user, http.MethodGet, "/api/v1/projects/acme/"+path, ""); code != tt.status {
				t.Errorf("GET %s as %s = %d, want %d; body = %v", path, tt.user, code, tt.status, body)
			}
		}
	}
}

func TestProjectUsage_Response(t *testing.T) {
	db := newQuotaMock()
	db.usage = &models.ProjectUsage{
		Quota:     models.ProjectQuota{MaxSizeBytes: 1000, WarnThresholdPercent: 80},
		SizeBytes: 500, ArtifactCount: 3,
		Repositories: []models.ProjectRepoUsage{
			{Name: "acme-a", Format: "npm", RepoType: "hosted", SizeBytes: 400, ArtifactCount: 2},
			{Name: "acme-p", Format: "npm", RepoType: "proxy", SizeBytes: 100, ArtifactCount: 1},
		},
	}

	code, body := projectJSON(t, newQuotaRouter(t, db), uView1, http.MethodGet, "/api/v1/projects/acme/usage", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d; body = %v", code, body)
	}

	if body["size_bytes"] != float64(500) {
		t.Fatalf("size_bytes = %v, want 500", body["size_bytes"])
	}

	repos, _ := body["repositories"].([]any)
	if len(repos) != 2 {
		t.Fatalf("repositories = %v, want 2", body["repositories"])
	}

	if hosted, _ := repos[0].(map[string]any); hosted["quota_enforced"] != true {
		t.Errorf("hosted repository quota_enforced = %v, want true", hosted["quota_enforced"])
	}

	if proxy, _ := repos[1].(map[string]any); proxy["quota_enforced"] != false {
		t.Errorf("proxy repository quota_enforced = %v, want false", proxy["quota_enforced"])
	}

	if q, _ := body["quota"].(map[string]any); q["enforced"] != true || q["usage_percent"] != float64(50) {
		t.Errorf("quota = %v, want enforced at 50%%", body["quota"])
	}
}

func TestProjectCleanupPolicies_Permissions(t *testing.T) {
	tests := []struct {
		user   string
		status int
	}{
		{uOwner1, http.StatusCreated},
		{uAdmin1, http.StatusCreated},
		{uMaint1, http.StatusForbidden},
		{uDev1, http.StatusForbidden},
		{uView1, http.StatusForbidden},
		{uNewbie, http.StatusNotFound},
	}

	for _, tt := range tests {
		db := newQuotaMock()
		router := newQuotaRouter(t, db)

		code, body := projectJSON(t, router, tt.user, http.MethodPost, "/api/v1/projects/acme/cleanup-policies", `{"name":"d","enabled":true,"keep_last_n":3}`)
		if code != tt.status {
			t.Errorf("POST as %s = %d, want %d; body = %v", tt.user, code, tt.status, body)
		}
	}
}

func TestProjectCleanupPolicies_Lifecycle(t *testing.T) {
	db := newQuotaMock()
	router := newQuotaRouter(t, db)

	code, body := projectJSON(t, router, uOwner1, http.MethodPost, "/api/v1/projects/acme/cleanup-policies", `{"name":"d","enabled":true,"keep_last_n":3}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d; body = %v", code, body)
	}

	id, _ := body["id"].(string)
	if body["project_id"] == nil || body["project_id"] == "" || body["repo_id"] != nil {
		t.Fatalf("created policy = %v, want a project_id and no repo_id", body)
	}

	if code, body := projectJSON(t, router, uOwner1, http.MethodPost, "/api/v1/projects/acme/cleanup-policies", `{"name":"bad","enabled":true}`); code != http.StatusBadRequest {
		t.Fatalf("a policy that keeps everything = %d (%v), want 400", code, body)
	}

	if code, body := projectJSON(t, router, uOwner1, http.MethodPut, "/api/v1/projects/acme/cleanup-policies/"+id, `{"keep_last_n":5}`); code != http.StatusOK || db.policies[id].KeepLastN == nil || *db.policies[id].KeepLastN != 5 {
		t.Fatalf("update = %d (%v), want keep_last_n 5", code, body)
	}

	if code, body := projectJSON(t, router, uMaint1, http.MethodDelete, "/api/v1/projects/acme/cleanup-policies/"+id, ""); code != http.StatusForbidden {
		t.Fatalf("maintainer delete = %d (%v), want 403", code, body)
	}

	if code, body := projectJSON(t, router, uOwner1, http.MethodDelete, "/api/v1/projects/acme/cleanup-policies/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d (%v), want 204", code, body)
	}

	if len(db.policies) != 0 {
		t.Fatalf("policy not deleted: %v", db.policies)
	}
}

// A policy of a repository or of another project is not reachable through this project.
func TestProjectCleanupPolicies_ScopedToProject(t *testing.T) {
	db := newQuotaMock()
	db.policies["00000000-0000-4000-8000-00000000d001"] = &models.CleanupPolicy{ID: "00000000-0000-4000-8000-00000000d001", RepositoryID: "repo-1", Name: "repo"}
	db.policies["00000000-0000-4000-8000-00000000d002"] = &models.CleanupPolicy{ID: "00000000-0000-4000-8000-00000000d002", ProjectID: "other-project", Name: "foreign"}
	router := newQuotaRouter(t, db)

	for id := range db.policies {
		if code, _ := projectJSON(t, router, uOwner1, http.MethodPut, "/api/v1/projects/acme/cleanup-policies/"+id, `{"enabled":false}`); code != http.StatusNotFound {
			t.Errorf("PUT foreign policy %s = %d, want 404", id, code)
		}

		if code, _ := projectJSON(t, router, uOwner1, http.MethodDelete, "/api/v1/projects/acme/cleanup-policies/"+id, ""); code != http.StatusNotFound {
			t.Errorf("DELETE foreign policy %s = %d, want 404", id, code)
		}
	}

	if len(db.policies) != 2 {
		t.Fatalf("a foreign policy was changed: %v", db.policies)
	}
}

type fakeProjectPreviewer struct{ ids []string }

func (f *fakeProjectPreviewer) PreviewProjectPolicy(_ context.Context, id string) ([]*worker.CleanupPreview, error) {
	f.ids = append(f.ids, id)

	return []*worker.CleanupPreview{
		{Repository: "a", Format: "npm", Rows: 2, Items: []worker.CleanupPreviewItem{{ID: "1", Name: "x", Version: "1.0.0"}}},
		{Repository: "b", Format: "cargo", Rows: 1},
	}, nil
}

// The dry run of a project default policy is for owners and admins, is scoped
// to the project, and reports every covered repository.
func TestProjectCleanupPolicies_DryRun(t *testing.T) {
	db := newQuotaMock()
	prev := &fakeProjectPreviewer{}

	log := newFixTestLogger(t)
	h := NewProjectsHandler(db, log, auth.NewEvaluator(db, log, false), auth.NewProjectSettings(db, false))
	h.SetCleanupPreviewer(prev)

	router := chi.NewRouter()
	h.RegisterRoutes(router)

	_, body := projectJSON(t, router, uOwner1, http.MethodPost, "/api/v1/projects/acme/cleanup-policies", `{"name":"d","enabled":true,"keep_last_n":3}`)
	id, _ := body["id"].(string)
	path := "/api/v1/projects/acme/cleanup-policies/" + id + "/dry-run"

	code, body := projectJSON(t, router, uOwner1, http.MethodPost, path, "")
	repos, _ := body["repositories"].([]any)

	if code != http.StatusOK || body["dry_run"] != true || body["rows"] != float64(3) || len(repos) != 2 || len(prev.ids) != 1 || prev.ids[0] != id {
		t.Fatalf("dry-run = %d %v (calls %v), want 200 with 3 rows over 2 repositories", code, body, prev.ids)
	}

	if code, _ := projectJSON(t, router, uMaint1, http.MethodPost, path, ""); code != http.StatusForbidden {
		t.Errorf("maintainer dry-run = %d, want 403", code)
	}

	if code, _ := projectJSON(t, router, uOwner1, http.MethodPost, "/api/v1/projects/acme/cleanup-policies/00000000-0000-4000-8000-00000000ffff/dry-run", ""); code != http.StatusNotFound {
		t.Errorf("unknown policy dry-run = %d, want 404", code)
	}
}
