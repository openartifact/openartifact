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
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// backupMockDB serves backup jobs like the uuid-keyed table: a lookup by an
// id that is not a UUID is an error, an unknown UUID is "no such job".
type backupMockDB struct {
	databasetest.BaseMockDB

	jobs []*models.BackupJob
}

func (db *backupMockDB) GetBackupJob(_ context.Context, id string) (*models.BackupJob, error) {
	if !isUUIDString(id) {
		return nil, errors.New("invalid input syntax for type uuid: " + id)
	}

	for _, j := range db.jobs {
		if j.ID == id {
			return j, nil
		}
	}

	return nil, nil
}

func (db *backupMockDB) ListBackupJobs(context.Context, int) ([]*models.BackupJob, error) {
	return db.jobs, nil
}

func backupRequest(t *testing.T, db *backupMockDB, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	r := chi.NewRouter()
	NewBackupHandler(db, log, auth.NewEvaluator(db, log, false), nil).RegisterAuthed(r)

	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(""))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	return rw
}

// A backup id that is not a UUID is a 404, not a database error.
func TestBackupMalformedIDIsNotFound(t *testing.T) {
	for _, suffix := range []string{"", "/download"} {
		for _, id := range []string{"0", "not-a-uuid", "null%2Cnull"} {
			path := "/api/v1/admin/backup/" + id + suffix

			if rw := backupRequest(t, &backupMockDB{}, http.MethodGet, path); rw.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404: %s", path, rw.Code, rw.Body.String())
			}
		}
	}
}
