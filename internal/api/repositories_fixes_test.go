// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

func newFixTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	return log
}

func TestValidFormatsMatchDatabaseConstraint(t *testing.T) {
	if !validFormats["go"] {
		t.Error(`validFormats must accept "go" (chk_repo_format)`)
	}

	if validFormats["gomod"] {
		t.Error(`validFormats must not accept "gomod": the database constraint rejects it`)
	}

	msg := validFormatList()
	for f := range validFormats {
		if !strings.Contains(msg, f) {
			t.Errorf("validFormatList() = %q does not mention %q", msg, f)
		}
	}
}

type repoCreateMockDB struct {
	databasetest.BaseMockDB

	createErr error
	targets   []*models.PermissionTarget
	created   *models.Repository
}

func (db *repoCreateMockDB) CreateRepositoryWithMembers(_ context.Context, repo *models.Repository, _ []string) error {
	db.created = repo
	return db.createErr
}

func (db *repoCreateMockDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return db.targets, nil
}

func adminRequest(db *repoCreateMockDB, method, target, body string) *http.Request {
	userID := "admin-1"
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))

	return req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "admin"}))
}

func adminTargets() []*models.PermissionTarget {
	return []*models.PermissionTarget{{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: []string{"manage", "read", "write"}}}
}

func TestRepositoriesCreateMapsUniqueViolationTo409(t *testing.T) {
	log := newFixTestLogger(t)
	db := &repoCreateMockDB{
		createErr: &pgconn.PgError{Code: "23505"},
		targets:   adminTargets(),
	}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"dup","format":"go"}`))

	if rw.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rw.Code, rw.Body.String())
	}

	db.createErr = errors.New("boom")
	rw = httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"other","format":"go"}`))

	if rw.Code != http.StatusInternalServerError {
		t.Fatalf("generic db error status = %d, want 500", rw.Code)
	}

	if strings.Contains(rw.Body.String(), "boom") {
		t.Error("internal error detail leaked to client")
	}
}

func TestRepositoriesCreatePersistsPublicFlag(t *testing.T) {
	log := newFixTestLogger(t)
	db := &repoCreateMockDB{targets: adminTargets()}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"pub","format":"go","public":true}`))

	if rw.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}

	if db.created == nil || !db.created.Public {
		t.Error("public flag was not passed to the database layer")
	}
}

func TestRepositoriesListClampsHugePage(t *testing.T) {
	log := newFixTestLogger(t)
	db := &repositoriesListMockDB{
		repos:   []*models.Repository{{Name: "a", Format: "rpm"}},
		targets: adminTargets(),
	}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))
	userID := "u"

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories?page=9223372036854775807&limit=100", nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "u"}))
	rw := httptest.NewRecorder()

	h.handleList(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}
}

func TestStorageBackendResponseRedactsSecrets(t *testing.T) {
	b := &models.StorageBackend{
		ID: "1", Name: "s3", Kind: "s3",
		Config: []byte(`{"bucket":"b","access_key":"AKIA","secret_key":"shh","secretKey":"shh2"}`),
	}

	resp := storageBackendToResponse(b)

	for _, k := range []string{"access_key", "secret_key", "secretKey"} {
		if _, ok := resp.Config[k]; ok {
			t.Errorf("secret %q returned by API", k)
		}
	}

	if resp.Config["has_secret_key"] != true || resp.Config["has_access_key"] != true {
		t.Errorf("has_* flags missing: %v", resp.Config)
	}

	if resp.Config["bucket"] != "b" {
		t.Error("non-secret config dropped")
	}
}

func TestPrepareStorageConfigKeepsOmittedSecrets(t *testing.T) {
	existing := []byte(`{"bucket":"b","access_key":"AK","secret_key":"SK"}`)

	out, err := prepareStorageConfig(t.Context(), "s3", []byte(`{"bucket":"b2","access_key":"","secret_key":""}`), existing)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["bucket"] != "b2" {
		t.Errorf("bucket = %v, want b2", got["bucket"])
	}

	for _, k := range []string{"access_key", "secret_key"} {
		if s, _ := got[k].(string); s == "" {
			t.Errorf("stored %s lost on update with empty value: %s", k, out)
		}
	}

	if _, err := prepareStorageConfig(t.Context(), "filesystem", []byte(`{"path":"../etc"}`), nil); err == nil {
		t.Error("path traversal accepted")
	}

	if _, err := prepareStorageConfig(t.Context(), "garage", []byte(`{"bucket":"b"}`), nil); err == nil {
		t.Error("garage without endpoint accepted")
	}
}
