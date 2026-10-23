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
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

type cleanupMockDB struct {
	database.BaseMockDB

	policy  *models.CleanupPolicy
	created int
	format  string // repository format; defaults to rpm
}

func (db *cleanupMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	format := db.format
	if format == "" {
		format = "rpm"
	}

	return &models.Repository{ID: "repo-1", Name: name, Format: format}, nil
}

func (db *cleanupMockDB) CreateCleanupPolicy(_ context.Context, p *models.CleanupPolicy) error {
	p.ID = "pol-1"
	db.policy = p
	db.created++

	return nil
}

func (db *cleanupMockDB) GetCleanupPolicy(_ context.Context, id string) (*models.CleanupPolicy, error) {
	if db.policy != nil && db.policy.ID == id {
		cp := *db.policy
		return &cp, nil
	}

	return nil, nil
}

func (db *cleanupMockDB) UpdateCleanupPolicy(_ context.Context, p *models.CleanupPolicy) error {
	db.policy = p
	return nil
}

func TestCleanupRejectsUnsupportedFormat(t *testing.T) {
	db := &cleanupMockDB{format: "bogus", policy: &models.CleanupPolicy{ID: "pol-1", RepositoryID: "repo-1", Name: "p"}}
	base := "/api/v1/repositories/r/cleanup-policies"

	rw := cleanupRequest(t, db, nil, http.MethodPost, base, `{"name":"p","keep_last_n":2}`)
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), codeUnsupportedFormat) || db.created != 0 {
		t.Fatalf("create: status = %d, body = %s, created = %d", rw.Code, rw.Body.String(), db.created)
	}

	rw = cleanupRequest(t, db, nil, http.MethodPut, base+"/pol-1", `{"keep_last_n":3}`)
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), codeUnsupportedFormat) {
		t.Fatalf("update: status = %d, body = %s", rw.Code, rw.Body.String())
	}
}

type fakeRunner struct {
	ids     []string
	err     error
	preview *worker.CleanupPreview
}

func (f *fakeRunner) TriggerPolicy(_ context.Context, id string) error {
	f.ids = append(f.ids, id)
	return f.err
}

func (f *fakeRunner) PreviewPolicy(_ context.Context, id string) (*worker.CleanupPreview, error) {
	f.ids = append(f.ids, "preview:"+id)
	return f.preview, f.err
}

func cleanupRequest(t *testing.T, db *cleanupMockDB, runner CleanupRunner, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	r := chi.NewRouter()
	NewCleanupHandler(db, log, auth.NewEvaluator(db, log, false), runner).RegisterAuthed(r)

	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	return rw
}

func TestCleanupCreateValidation(t *testing.T) {
	const path = "/api/v1/repositories/r/cleanup-policies"

	db := &cleanupMockDB{}

	for name, body := range map[string]string{
		"no criteria":         `{"name":"p","enabled":true}`,
		"zero keep_last_n":    `{"name":"p","keep_last_n":0}`,
		"negative keep_last":  `{"name":"p","keep_last_n":-4}`,
		"zero keep_since":     `{"name":"p","keep_since_days":0}`,
		"negative keep_since": `{"name":"p","keep_since_days":-1}`,
		"bad regex":           `{"name":"p","keep_last_n":2,"name_pattern":"("}`,
		"bad cron":            `{"name":"p","keep_last_n":2,"schedule_cron":"nope"}`,
		"downloaded":          `{"name":"p","keep_last_n":2,"keep_downloaded_since_days":3}`,
		"no name":             `{"keep_last_n":2}`,
	} {
		if rw := cleanupRequest(t, db, nil, http.MethodPost, path, body); rw.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, body = %s", name, rw.Code, rw.Body.String())
		}
	}

	if db.created != 0 {
		t.Fatalf("invalid policies were stored: %d", db.created)
	}

	rw := cleanupRequest(t, db, nil, http.MethodPost, path, `{"name":"p","keep_last_n":2,"schedule_cron":"0 3 * * 1"}`)
	if rw.Code != http.StatusCreated || db.created != 1 {
		t.Fatalf("valid policy rejected: %d %s", rw.Code, rw.Body.String())
	}
}

func TestCleanupUpdateRejectsUnsafeValues(t *testing.T) {
	n := 5
	db := &cleanupMockDB{policy: &models.CleanupPolicy{ID: "pol-1", RepositoryID: "repo-1", Name: "p", Enabled: true, KeepLastN: &n}}

	rw := cleanupRequest(t, db, nil, http.MethodPut, "/api/v1/repositories/r/cleanup-policies/pol-1", `{"keep_last_n":0}`)
	if rw.Code != http.StatusBadRequest || *db.policy.KeepLastN != 5 {
		t.Fatalf("status = %d, keep_last_n = %d", rw.Code, *db.policy.KeepLastN)
	}
}

func TestCleanupRunTriggersWorker(t *testing.T) {
	n := 5
	db := &cleanupMockDB{policy: &models.CleanupPolicy{ID: "pol-1", RepositoryID: "repo-1", Name: "p", KeepLastN: &n}}
	runner := &fakeRunner{}

	rw := cleanupRequest(t, db, runner, http.MethodPost, "/api/v1/repositories/r/cleanup-policies/pol-1/run", "")
	if rw.Code != http.StatusAccepted || len(runner.ids) != 1 || runner.ids[0] != "pol-1" {
		t.Fatalf("status = %d, triggered = %v", rw.Code, runner.ids)
	}

	runner.err = worker.ErrPolicyBusy
	if rw := cleanupRequest(t, db, runner, http.MethodPost, "/api/v1/repositories/r/cleanup-policies/pol-1/run", ""); rw.Code != http.StatusConflict {
		t.Fatalf("busy status = %d", rw.Code)
	}

	if rw := cleanupRequest(t, db, nil, http.MethodPost, "/api/v1/repositories/r/cleanup-policies/pol-1/run", ""); rw.Code != http.StatusNotImplemented {
		t.Fatalf("nil runner status = %d", rw.Code)
	}
}

func TestCleanupDryRunReportsWithoutRunning(t *testing.T) {
	const path = "/api/v1/repositories/r/cleanup-policies/pol-1/dry-run"

	n := 5
	db := &cleanupMockDB{policy: &models.CleanupPolicy{ID: "pol-1", RepositoryID: "repo-1", Name: "p", KeepLastN: &n}}
	runner := &fakeRunner{preview: &worker.CleanupPreview{
		Repository: "r", Format: "npm", Rows: 2, Truncated: true,
		Items: []worker.CleanupPreviewItem{{ID: "a", Name: "left-pad", Version: "1.0.0"}},
	}}

	rw := cleanupRequest(t, db, runner, http.MethodPost, path, "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rw.Code, rw.Body)
	}

	for _, want := range []string{`"dry_run":true`, `"rows":2`, `"truncated":true`, `"name":"left-pad"`, `"version":"1.0.0"`} {
		if !strings.Contains(rw.Body.String(), want) {
			t.Errorf("body %s lacks %s", rw.Body, want)
		}
	}

	if len(runner.ids) != 1 || runner.ids[0] != "preview:pol-1" {
		t.Errorf("runner calls = %v, want only the preview", runner.ids)
	}

	runner.err = models.ErrInvalidCleanupPolicy
	if rw := cleanupRequest(t, db, runner, http.MethodPost, path, ""); rw.Code != http.StatusBadRequest {
		t.Errorf("invalid policy status = %d", rw.Code)
	}

	if rw := cleanupRequest(t, db, runner, http.MethodPost, "/api/v1/repositories/r/cleanup-policies/nope/dry-run", ""); rw.Code != http.StatusNotFound {
		t.Errorf("unknown policy status = %d", rw.Code)
	}

	if rw := cleanupRequest(t, db, nil, http.MethodPost, path, ""); rw.Code != http.StatusNotImplemented {
		t.Errorf("nil runner status = %d", rw.Code)
	}
}
