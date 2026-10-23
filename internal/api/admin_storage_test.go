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
	"github.com/openartifact/openartifact/pkg/logger"
)

type storageUpdateMockDB struct {
	database.BaseMockDB

	backend *models.StorageBackend
	busy    string
	updated bool
}

func (m *storageUpdateMockDB) GetStorageBackend(context.Context, string) (*models.StorageBackend, error) {
	c := *m.backend

	return &c, nil
}

func (m *storageUpdateMockDB) UpdateStorageBackend(context.Context, *models.StorageBackend) error {
	m.updated = true
	return nil
}

func (m *storageUpdateMockDB) StorageBackendBusyReason(context.Context, string) (string, error) {
	return m.busy, nil
}

func TestStorageBackendUpdateConflictsWhenBackendBusy(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		busy   string
		body   string
		status int
		s3     bool // the stored backend is an s3 bucket "b" instead of a filesystem path
	}{
		{"config change while busy", "holds blobs", `{"config":{"path":"/other"}}`, http.StatusConflict, false},
		{"kind change while busy", "holds blobs", `{"kind":"gcs","config":{"bucket":"b"}}`, http.StatusConflict, false},
		{"rename while busy", "holds blobs", `{"name":"renamed"}`, http.StatusOK, false},
		{"same config while busy", "holds blobs", `{"config":{"path":"/data"}}`, http.StatusOK, false},
		{"rotate secret while busy", "holds blobs", `{"config":{"bucket":"b","access_key":"AKIAEXAMPLE","secret_key":"newsecret1"}}`, http.StatusOK, true},
		{"change bucket while busy", "holds blobs", `{"config":{"bucket":"other","access_key":"AKIAEXAMPLE","secret_key":"newsecret1"}}`, http.StatusConflict, true},
		{"config change while idle", "", `{"config":{"path":"/other"}}`, http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &models.StorageBackend{ID: "b1", Name: "cold", Kind: "filesystem", Config: []byte(`{"path":"/data"}`)}
			if tc.s3 {
				backend.Kind, backend.Config = "s3", []byte(`{"bucket":"b","access_key":"AKIAEXAMPLE","secret_key":"enc:old"}`)
			}

			db := &storageUpdateMockDB{backend: backend, busy: tc.busy}

			r := chi.NewRouter()
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsLegacy: true})))
				})
			})
			NewAdminStorageHandler(db, log, nil).Register(r)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/api/v1/admin/storage/cold", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}

			if db.updated != (tc.status == http.StatusOK) {
				t.Errorf("updated = %v with status %d", db.updated, rec.Code)
			}
		})
	}
}

type fakeHealthStore struct{ err error }

func (f fakeHealthStore) HealthCheck(context.Context) error { return f.err }

func TestStorageBackendTestRunsRealChecksForEveryKind(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	// gcs and azure used to answer 200 {"error":"not_implemented"}; with
	// incomplete credentials they must now fail validation of the config.
	for kind, config := range map[string]string{
		"gcs":   `{"bucket":"b"}`,
		"azure": `{"account":"acct","container":"c"}`,
	} {
		t.Run(kind, func(t *testing.T) {
			db := &storageUpdateMockDB{backend: &models.StorageBackend{ID: "b1", Name: "x", Kind: kind, Config: []byte(config)}}

			r := chi.NewRouter()
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsLegacy: true})))
				})
			})
			NewAdminStorageHandler(db, log, nil).Register(r)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/admin/storage/x/test", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "invalid_config") || strings.Contains(body, "not_implemented") {
				t.Fatalf("status = %d, body = %s", rec.Code, body)
			}
		})
	}
}

func TestHealthCheckStoreReportsConnectivity(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	h := NewAdminStorageHandler(&storageUpdateMockDB{}, log, nil)

	if res := h.healthCheckStore(context.Background(), "x", "azure", fakeHealthStore{}); !res.ok {
		t.Errorf("healthy store: %+v", res)
	}

	res := h.healthCheckStore(context.Background(), "x", "gcs", fakeHealthStore{err: context.DeadlineExceeded})
	if res.ok || res.code != "connection_failed" {
		t.Errorf("failing store: %+v", res)
	}
}
