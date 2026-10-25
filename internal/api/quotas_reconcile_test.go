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
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

type reconcileMockDB struct {
	databasetest.BaseMockDB

	known      bool
	repoErr    error
	reconErr   error
	reconciled []string
}

func (db *reconcileMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if db.repoErr != nil || !db.known {
		return nil, db.repoErr
	}

	return &models.Repository{ID: "repo-1", Name: name}, nil
}

func (db *reconcileMockDB) ReconcileStorageUsage(_ context.Context, repoID string) (*models.StorageQuota, error) {
	db.reconciled = append(db.reconciled, repoID)
	if db.reconErr != nil {
		return nil, db.reconErr
	}

	return &models.StorageQuota{
		RepositoryID: repoID, MaxSizeBytes: 1000, CurrentSizeBytes: 250, CurrentArtifactCount: 3,
	}, nil
}

func reconcileRequest(t *testing.T, db *reconcileMockDB, p *auth.Principal) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	r := chi.NewRouter()
	NewQuotasHandler(log, db, auth.NewEvaluator(db, log, false)).RegisterRoutes(r, "/api/v1/repositories/{name}")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories/r/quota/reconcile", nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	return rw
}

func TestReconcileQuotaHandler(t *testing.T) {
	admin := &auth.Principal{Username: "admin", IsLegacy: true}

	tests := []struct {
		name          string
		db            *reconcileMockDB
		principal     *auth.Principal
		wantStatus    int
		wantReconcile bool
	}{
		{"admin recomputes and gets the quota back", &reconcileMockDB{known: true}, admin, http.StatusOK, true},
		{"anonymous is refused before anything runs", &reconcileMockDB{known: true}, &auth.Principal{IsAnonymous: true}, http.StatusForbidden, false},
		{"unknown repository", &reconcileMockDB{known: false}, admin, http.StatusNotFound, false},
		{"repository lookup failure", &reconcileMockDB{repoErr: errors.New("db down")}, admin, http.StatusInternalServerError, false},
		{"reconcile failure", &reconcileMockDB{known: true, reconErr: errors.New("db down")}, admin, http.StatusInternalServerError, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rw := reconcileRequest(t, tc.db, tc.principal)
			if rw.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rw.Code, tc.wantStatus, rw.Body.String())
			}

			if got := len(tc.db.reconciled) > 0; got != tc.wantReconcile {
				t.Errorf("reconcile called = %v, want %v", got, tc.wantReconcile)
			}

			if tc.wantStatus != http.StatusOK {
				return
			}

			var body struct {
				MaxSizeBytes         int64 `json:"max_size_bytes"`
				CurrentSizeBytes     int64 `json:"current_size_bytes"`
				CurrentArtifactCount int   `json:"current_artifact_count"`
			}
			if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if body.MaxSizeBytes != 1000 || body.CurrentSizeBytes != 250 || body.CurrentArtifactCount != 3 {
				t.Errorf("response = %+v, want the reconciled quota (limit 1000, 250 bytes, 3 artifacts)", body)
			}
		})
	}
}
