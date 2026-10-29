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

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

var errNotImplemented = errors.New("not implemented")

type storageUpdateMockDB struct {
	AdminStorageStore

	backend *models.StorageBackend
	busy    string
	updated bool
	saved   *models.StorageBackend
}

// UpdateStorageBackendStatus is not exercised by the tests.
func (*storageUpdateMockDB) UpdateStorageBackendStatus(context.Context, string, string, *string) error {
	return errNotImplemented
}

func (m *storageUpdateMockDB) GetStorageBackend(context.Context, string) (*models.StorageBackend, error) {
	c := *m.backend

	return &c, nil
}

func (m *storageUpdateMockDB) UpdateStorageBackend(_ context.Context, b *models.StorageBackend) error {
	m.updated = true
	m.saved = b

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

			req := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPatch,
				"/api/v1/admin/storage/cold",
				strings.NewReader(tc.body),
			)
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

// A PATCH that omits config (a rename, a kind-only change) re-validates the
// stored config, whose secrets are enc:v1: ciphertext. That must not be
// mistaken for a submitted plaintext, and the ciphertext must survive.
func TestStorageBackendPatchWithoutConfigKeepsEncryptedSecrets(t *testing.T) {
	if err := crypto.Init("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	sealed, err := crypto.Encrypt("topsecret")
	if err != nil || !crypto.IsEncrypted(sealed) {
		t.Fatalf("setup: Encrypt = %q, %v", sealed, err)
	}

	stored, err := json.Marshal(map[string]any{"bucket": "b", "access_key": sealed, "secret_key": sealed})
	if err != nil {
		t.Fatal(err)
	}

	db := &storageUpdateMockDB{backend: &models.StorageBackend{ID: "b1", Name: "cold", Kind: "s3", Config: stored}}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsLegacy: true})))
		})
	})
	NewAdminStorageHandler(db, log, nil).Register(r)

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPatch, "/api/v1/admin/storage/cold", strings.NewReader(`{"name":"renamed"}`),
	)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	if db.saved == nil || db.saved.Name != "renamed" {
		t.Fatalf("saved backend = %+v, want it renamed", db.saved)
	}

	var cfg map[string]any
	if err := json.Unmarshal(db.saved.Config, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg["bucket"] != "b" || cfg["access_key"] != sealed || cfg["secret_key"] != sealed {
		t.Errorf("saved config = %s, want the bucket and both stored ciphertexts untouched", db.saved.Config)
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
			if rec.Code != http.StatusOK || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "invalid_config") ||
				strings.Contains(body, "not_implemented") {
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

type storageUsageMockDB struct {
	AdminStorageStore
}

func (storageUsageMockDB) ListStorageBackends(context.Context) ([]*models.StorageBackend, error) {
	return []*models.StorageBackend{
		{ID: "b1", Name: "cold", Kind: "filesystem", Config: []byte(`{"path":"/data"}`)},
		{ID: "b2", Name: "empty", Kind: "filesystem", Config: []byte(`{"path":"/e"}`)},
	}, nil
}

func (storageUsageMockDB) GetStorageBackend(_ context.Context, name string) (*models.StorageBackend, error) {
	return &models.StorageBackend{ID: "b1", Name: name, Kind: "filesystem", Config: []byte(`{"path":"/data"}`)}, nil
}

func (storageUsageMockDB) StorageTierUsage(context.Context) ([]models.TierUsage, error) {
	return []models.TierUsage{{BackendID: "", Blobs: 9, Bytes: 90}, {BackendID: "b1", Blobs: 3, Bytes: 300}}, nil
}

func TestStorageBackendsReportUsage(t *testing.T) {
	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsLegacy: true})))
		})
	})
	NewAdminStorageHandler(&storageUsageMockDB{}, log, nil).Register(r)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))

		return rec
	}

	list := get("/api/v1/admin/storage")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body)
	}

	var body struct {
		Items []struct {
			Name  string `json:"name"`
			Usage struct {
				BlobCount int64 `json:"blob_count"`
				SizeBytes int64 `json:"size_bytes"`
			} `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if len(body.Items) != 2 || body.Items[0].Usage.BlobCount != 3 || body.Items[0].Usage.SizeBytes != 300 {
		t.Errorf("busy backend usage wrong: %+v", body.Items)
	}

	if body.Items[1].Usage.BlobCount != 0 || body.Items[1].Usage.SizeBytes != 0 {
		t.Errorf("idle backend must report zeros: %+v", body.Items[1])
	}

	one := get("/api/v1/admin/storage/cold")
	if one.Code != http.StatusOK || !strings.Contains(one.Body.String(), `"usage":{"blob_count":3,"size_bytes":300}`) {
		t.Errorf("get: %d %s", one.Code, one.Body)
	}
}
