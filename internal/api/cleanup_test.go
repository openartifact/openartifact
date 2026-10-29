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
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// cleanupMockDBStore is what cleanupMockDB is used as.
type cleanupMockDBStore interface {
	CleanupStore
	auth.EvaluatorStore
}

type cleanupMockDB struct {
	cleanupMockDBStore

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
	p.ID = "5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10"
	db.policy = p
	db.created++

	return nil
}

// GetCleanupPolicy rejects ids that are not UUIDs the way the uuid column does,
// so a handler that forwards a raw path segment fails loudly.
func (db *cleanupMockDB) GetCleanupPolicy(_ context.Context, id string) (*models.CleanupPolicy, error) {
	if !isUUIDString(id) {
		return nil, errors.New("invalid input syntax for type uuid: " + id)
	}

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

// A policy id that is not a UUID is a 404 on every id route, not a database error.
func TestCleanupMalformedIDIsNotFound(t *testing.T) {
	for _, rt := range []struct{ method, suffix, body string }{
		{http.MethodGet, "", ""},
		{http.MethodPut, "", `{"keep_last_n":3}`},
		{http.MethodDelete, "", ""},
		{http.MethodPost, "/run", ""},
		{http.MethodPost, "/dry-run", ""},
	} {
		for _, id := range []string{"0", "not-a-uuid"} {
			path := "/api/v1/repositories/r/cleanup-policies/" + id + rt.suffix

			if rw := cleanupRequest(t, &cleanupMockDB{}, &fakeRunner{}, rt.method, path, rt.body); rw.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404: %s", rt.method, path, rw.Code, rw.Body.String())
			}
		}
	}
}

func TestCleanupRejectsUnsupportedFormat(t *testing.T) {
	db := &cleanupMockDB{
		format: "bogus",
		policy: &models.CleanupPolicy{ID: "5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10", RepositoryID: "repo-1", Name: "p"},
	}
	base := "/api/v1/repositories/r/cleanup-policies"

	rw := cleanupRequest(t, db, nil, http.MethodPost, base, `{"name":"p","keep_last_n":2}`)
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), codeUnsupportedFormat) || db.created != 0 {
		t.Fatalf("create: status = %d, body = %s, created = %d", rw.Code, rw.Body.String(), db.created)
	}

	rw = cleanupRequest(t, db, nil, http.MethodPut, base+"/5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10", `{"keep_last_n":3}`)
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

	// A format supports cleanup policies when it registered a storage descriptor.
	storage.Reset()
	storage.Register("rpm", storage.Storage{})
	t.Cleanup(storage.Reset)

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
	db := &cleanupMockDB{
		policy: &models.CleanupPolicy{
			ID:           "5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10",
			RepositoryID: "repo-1",
			Name:         "p",
			Enabled:      true,
			KeepLastN:    &n,
		},
	}

	rw := cleanupRequest(
		t,
		db,
		nil,
		http.MethodPut,
		"/api/v1/repositories/r/cleanup-policies/5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10",
		`{"keep_last_n":0}`,
	)
	if rw.Code != http.StatusBadRequest || *db.policy.KeepLastN != 5 {
		t.Fatalf("status = %d, keep_last_n = %d", rw.Code, *db.policy.KeepLastN)
	}
}

func TestCleanupRunTriggersWorker(t *testing.T) {
	n := 5
	db := &cleanupMockDB{
		policy: &models.CleanupPolicy{ID: "5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10", RepositoryID: "repo-1", Name: "p", KeepLastN: &n},
	}
	runner := &fakeRunner{}

	rw := cleanupRequest(
		t,
		db,
		runner,
		http.MethodPost,
		"/api/v1/repositories/r/cleanup-policies/5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10/run",
		"",
	)
	if rw.Code != http.StatusAccepted || len(runner.ids) != 1 || runner.ids[0] != "5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10" {
		t.Fatalf("status = %d, triggered = %v", rw.Code, runner.ids)
	}

	runner.err = worker.ErrPolicyBusy
	if rw := cleanupRequest(
		t,
		db,
		runner,
		http.MethodPost,
		"/api/v1/repositories/r/cleanup-policies/5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10/run",
		"",
	); rw.Code != http.StatusConflict {
		t.Fatalf("busy status = %d", rw.Code)
	}

	if rw := cleanupRequest(
		t,
		db,
		nil,
		http.MethodPost,
		"/api/v1/repositories/r/cleanup-policies/5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10/run",
		"",
	); rw.Code != http.StatusNotImplemented {
		t.Fatalf("nil runner status = %d", rw.Code)
	}
}

func TestCleanupDryRunReportsWithoutRunning(t *testing.T) {
	const path = "/api/v1/repositories/r/cleanup-policies/5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10/dry-run"

	n := 5
	db := &cleanupMockDB{
		policy: &models.CleanupPolicy{ID: "5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10", RepositoryID: "repo-1", Name: "p", KeepLastN: &n},
	}
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

	if len(runner.ids) != 1 || runner.ids[0] != "preview:5d0c6a0e-8f6b-4f6e-9c53-2b1d2f7d4a10" {
		t.Errorf("runner calls = %v, want only the preview", runner.ids)
	}

	runner.err = models.ErrInvalidCleanupPolicy
	if rw := cleanupRequest(t, db, runner, http.MethodPost, path, ""); rw.Code != http.StatusBadRequest {
		t.Errorf("invalid policy status = %d", rw.Code)
	}

	if rw := cleanupRequest(
		t,
		db,
		runner,
		http.MethodPost,
		"/api/v1/repositories/r/cleanup-policies/7b9e1c52-0a3d-4e8f-8d11-6c4a9e2b5f33/dry-run",
		"",
	); rw.Code != http.StatusNotFound {
		t.Errorf("unknown policy status = %d", rw.Code)
	}

	if rw := cleanupRequest(t, db, nil, http.MethodPost, path, ""); rw.Code != http.StatusNotImplemented {
		t.Errorf("nil runner status = %d", rw.Code)
	}
}
