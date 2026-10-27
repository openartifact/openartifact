// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
)

// healthStore is a blob store that only answers HealthCheck, counting probes.
type healthStore struct {
	storage.BlobStore

	err    error
	probes atomic.Int32
}

func (s *healthStore) HealthCheck(context.Context) error {
	s.probes.Add(1)
	return s.err
}

const (
	primaryAccessKey = "test-access-key-id"
	primarySecretKey = "primary-secret-key-value"
	primaryPassword  = "test-oci-password"
)

func primaryS3Config() storage.StorageConfig {
	return storage.StorageConfig{
		Type:          "s3",
		Endpoint:      "https://endpoint-user:endpoint-userinfo-pw@s3.example.com:9000/base?X-Amz-Signature=sig-in-query",
		Bucket:        "artifacts-prod",
		Region:        "eu-west-1",
		AccessKey:     primaryAccessKey,
		SecretKey:     primarySecretKey,
		GCSHMACSecret: "hmac-secret-value",
		OCIPassword:   primaryPassword,
	}
}

type primaryRouter struct {
	do func(method, path, body string) *httptest.ResponseRecorder
}

// setupPrimaryRouter wires the storage handler with a configured primary store
// and the given placement usage ("" = primary).
func setupPrimaryRouter(t *testing.T, cfg storage.StorageConfig, store storage.BlobStore, usage []models.TierUsage) primaryRouter {
	t.Helper()

	db := newMockDatabase()
	db.tierUsage = usage

	log := newHandlerLogger(t)
	handler := api.NewAdminStorageHandler(db, log, auth.NewEvaluator(db, log, false)).WithPrimary(cfg, store)

	return routeStorage(t, db, handler)
}

func routeStorage(t *testing.T, db *MockDatabase, h *api.AdminStorageHandler) primaryRouter {
	t.Helper()

	router, principal := setupStorageRouterWith(t, db, h)

	return primaryRouter{do: func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewBufferString(body))
		req = withStoragePrincipal(req, principal)
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)

		return rw
	}}
}

func decodeJSONMap(t *testing.T, rw *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rw.Body.String(), err)
	}

	return out
}

func TestAdminStoragePrimary_ListExposesConfiguredStoreWithoutSecrets(t *testing.T) {
	store := &healthStore{}
	r := setupPrimaryRouter(t, primaryS3Config(), store, []models.TierUsage{
		{BackendID: "", Blobs: 42, Bytes: 1 << 20},
		{BackendID: "some-tier", Blobs: 7, Bytes: 999},
	})

	rw := r.do("GET", "/api/v1/admin/storage", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	secrets := []string{primaryAccessKey, primarySecretKey, "endpoint-userinfo-pw", "sig-in-query", "hmac-secret-value", primaryPassword}

	for _, secret := range secrets {
		if bytes.Contains(rw.Body.Bytes(), []byte(secret)) {
			t.Errorf("response leaks %q: %s", secret, rw.Body.String())
		}
	}

	resp := decodeJSONMap(t, rw)
	if total := resp["total"].(float64); total != 0 {
		t.Errorf("total = %v, want 0: the primary store is not a stored backend", total)
	}

	primary, ok := resp["primary"].(map[string]any)
	if !ok {
		t.Fatalf("no primary object in %s", rw.Body.String())
	}

	if primary["name"] != "primary" || primary["kind"] != "s3" || primary["source"] != "config" {
		t.Errorf("primary identity = %v", primary)
	}

	loc, _ := primary["location"].(map[string]any)
	want := map[string]string{"endpoint": "https://s3.example.com:9000/base", "bucket": "artifacts-prod", "region": "eu-west-1"}

	if len(loc) != len(want) {
		t.Errorf("location = %v, want %v", loc, want)
	}

	for k, v := range want {
		if loc[k] != v {
			t.Errorf("location[%s] = %v, want %q", k, loc[k], v)
		}
	}

	if primary["status"] != "healthy" || primary["status_message"] != nil || primary["last_checked_at"] == nil {
		t.Errorf("status = %v / %v / %v", primary["status"], primary["status_message"], primary["last_checked_at"])
	}

	usage, _ := primary["usage"].(map[string]any)
	if usage["blob_count"] != float64(42) || usage["size_bytes"] != float64(1<<20) {
		t.Errorf("usage = %v, want only the blobs on the primary store (42, 1 MiB)", usage)
	}
}

func TestAdminStoragePrimary_FilesystemLocationAndRealHealth(t *testing.T) {
	dir := t.TempDir()

	store, err := storage.NewFilesystemBlobStore(storage.StorageConfig{Type: "filesystem", Path: dir})
	if err != nil {
		t.Fatal(err)
	}

	r := setupPrimaryRouter(t, storage.StorageConfig{Type: "filesystem", Path: dir}, store, nil)

	resp := decodeJSONMap(t, r.do("GET", "/api/v1/admin/storage/primary", ""))

	if loc, _ := resp["location"].(map[string]any); loc["path"] != dir || len(loc) != 1 {
		t.Errorf("location = %v, want only the path", loc)
	}

	if resp["status"] != "healthy" {
		t.Errorf("status = %v, want healthy for a writable directory", resp["status"])
	}

	if usage, _ := resp["usage"].(map[string]any); usage["blob_count"] != float64(0) {
		t.Errorf("usage = %v, want zeros", usage)
	}
}

func TestAdminStoragePrimary_UnreachableUsesFixedMessageAndCaches(t *testing.T) {
	store := &healthStore{err: errors.New("dial tcp 10.0.0.7:9000: secret-internal-detail")}
	r := setupPrimaryRouter(t, primaryS3Config(), store, nil)

	rw := r.do("GET", "/api/v1/admin/storage/primary", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	if bytes.Contains(rw.Body.Bytes(), []byte("secret-internal-detail")) {
		t.Errorf("the raw probe error reaches the response: %s", rw.Body.String())
	}

	resp := decodeJSONMap(t, rw)
	if resp["status"] != "unreachable" || resp["status_message"] == nil {
		t.Errorf("status = %v / %v, want unreachable with a message", resp["status"], resp["status_message"])
	}

	// Reads share the cached probe; only the test endpoint probes again.
	r.do("GET", "/api/v1/admin/storage", "")
	r.do("GET", "/api/v1/admin/storage/primary", "")

	if n := store.probes.Load(); n != 1 {
		t.Errorf("reads probed the store %d times, want 1 (cached)", n)
	}

	rw = r.do("POST", "/api/v1/admin/storage/primary/test", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("test: expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	test := decodeJSONMap(t, rw)
	if test["ok"] != false || test["error"] != "connection_failed" || test["message"] == "" {
		t.Errorf("test result = %v", test)
	}

	if n := store.probes.Load(); n != 2 {
		t.Errorf("test endpoint: %d probes in total, want 2", n)
	}

	// The test refreshed the cache: recovery shows up on the next read.
	store.err = nil

	if test := decodeJSONMap(t, r.do("POST", "/api/v1/admin/storage/primary/test", "")); test["ok"] != true {
		t.Errorf("test after recovery = %v", test)
	}

	if resp := decodeJSONMap(t, r.do("GET", "/api/v1/admin/storage/primary", "")); resp["status"] != "healthy" {
		t.Errorf("status after a successful test = %v, want healthy", resp["status"])
	}
}

func TestAdminStoragePrimary_ReadOnlyAndNameReserved(t *testing.T) {
	r := setupPrimaryRouter(t, primaryS3Config(), &healthStore{}, nil)

	for _, method := range []string{"PATCH", "DELETE"} {
		rw := r.do(method, "/api/v1/admin/storage/primary", `{"name":"x"}`)
		if rw.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s primary: expected 405, got %d: %s", method, rw.Code, rw.Body.String())
		}

		if got := rw.Header().Get("Allow"); got != http.MethodGet {
			t.Errorf("%s primary: Allow = %q, want GET", method, got)
		}
	}

	rw := r.do("POST", "/api/v1/admin/storage", `{"name":"primary","kind":"filesystem","config":{"path":"/tmp/x"}}`)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("creating a backend named primary: expected 400, got %d: %s", rw.Code, rw.Body.String())
	}

	rw = r.do("POST", "/api/v1/admin/storage", `{"name":"cold","kind":"filesystem","config":{"path":"/tmp/x"}}`)
	if rw.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rw.Code, rw.Body.String())
	}

	if rw = r.do("PATCH", "/api/v1/admin/storage/cold", `{"name":"primary"}`); rw.Code != http.StatusBadRequest {
		t.Errorf("renaming a backend to primary: expected 400, got %d: %s", rw.Code, rw.Body.String())
	}
}

func TestAdminStoragePrimary_AbsentWhenNotWired(t *testing.T) {
	db := newMockDatabase()
	log := newHandlerLogger(t)
	r := routeStorage(t, db, api.NewAdminStorageHandler(db, log, auth.NewEvaluator(db, log, false)))

	if _, ok := decodeJSONMap(t, r.do("GET", "/api/v1/admin/storage", ""))["primary"]; ok {
		t.Error("list carries a primary object although none is wired")
	}

	for _, req := range [][2]string{{"GET", "/api/v1/admin/storage/primary"}, {"POST", "/api/v1/admin/storage/primary/test"}} {
		if rw := r.do(req[0], req[1], ""); rw.Code != http.StatusNotFound {
			t.Errorf("%s %s: expected 404, got %d", req[0], req[1], rw.Code)
		}
	}
}

func TestAdminStoragePrimary_RequiresAdmin(t *testing.T) {
	db := newMockDatabase()
	log := newHandlerLogger(t)
	h := api.NewAdminStorageHandler(db, log, auth.NewEvaluator(db, log, false)).WithPrimary(primaryS3Config(), &healthStore{})
	router, _ := setupStorageRouterWith(t, db, h)

	user, err := db.CreateUser(context.Background(), &models.User{ID: "primary-regular", Username: "regular"})
	if err != nil {
		t.Fatal(err)
	}

	for _, req := range [][2]string{
		{"GET", "/api/v1/admin/storage/primary"}, {"POST", "/api/v1/admin/storage/primary/test"},
		{"PATCH", "/api/v1/admin/storage/primary"}, {"DELETE", "/api/v1/admin/storage/primary"},
	} {
		r := httptest.NewRequestWithContext(context.Background(), req[0], req[1], nil)
		r = withStoragePrincipal(r, &auth.Principal{UserID: &user.ID, Username: user.Username})
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, r)

		if rw.Code != http.StatusForbidden {
			t.Errorf("%s %s: expected 403, got %d", req[0], req[1], rw.Code)
		}
	}
}
