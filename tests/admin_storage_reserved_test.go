// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
)

const storagePrefix = "/api/v1/admin/storage/"

// storageRoutes mounts the storage and the replication handlers the way the
// server does, so they share the /api/v1/admin/storage prefix.
func storageRoutes(t *testing.T, db *MockDatabase) (chi.Router, primaryRouter) {
	t.Helper()

	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	router, principal := setupStorageRouterWith(t, db, api.NewAdminStorageHandler(db, log, eval))
	api.NewAdminReplicationHandler(nil, log, eval).Register(router)

	return router, primaryRouter{do: func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewBufferString(body))
		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, withStoragePrincipal(req, principal))

		return rw
	}}
}

// Every static segment under /admin/storage shadows a record of that name, so
// each must be refused as a name. This walks the mounted routes: a route added
// later without reserving its segment fails here.
func TestAdminStorage_EveryStaticSegmentIsReserved(t *testing.T) {
	router, r := storageRoutes(t, newMockDatabase())

	var segments []string

	err := chi.Walk(router, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		rest, ok := strings.CutPrefix(route, storagePrefix)
		if !ok {
			return nil
		}

		if seg, _, _ := strings.Cut(rest, "/"); !strings.HasPrefix(seg, "{") {
			segments = append(segments, seg)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(segments) == 0 {
		t.Fatal("found no static route under /admin/storage")
	}

	for _, seg := range segments {
		body := `{"name":"` + seg + `","kind":"filesystem","config":{"path":"/tmp/x"}}`
		if rw := r.do("POST", "/api/v1/admin/storage", body); rw.Code != http.StatusBadRequest {
			t.Errorf("creating a backend named %q: expected 400, got %d: %s", seg, rw.Code, rw.Body.String())
		}
	}

	for _, name := range []string{"primary", "replication"} {
		if !slices.Contains(segments, name) {
			t.Errorf("segment %q is not routed statically any more: update the reserved names", name)
		}
	}
}

func TestAdminStorage_RenameToReplicationRefused(t *testing.T) {
	db := newMockDatabase()
	seedStorageBackend(t, db, "cold", "filesystem", map[string]any{"path": "/tmp/x"})

	_, r := storageRoutes(t, db)

	if rw := r.do("PATCH", storagePrefix+"cold", `{"name":"replication"}`); rw.Code != http.StatusBadRequest {
		t.Fatalf("renaming a backend to replication: expected 400, got %d: %s", rw.Code, rw.Body.String())
	}
}

// A record named "replication" from before the name was reserved stays in the
// list, and PATCH and DELETE by name still reach it: it can be renamed (the
// way out) or deleted. GET and the test action are taken by the replication
// endpoints.
func TestAdminStorage_ExistingReplicationRecordStaysReachable(t *testing.T) {
	db := newMockDatabase()
	seedStorageBackend(t, db, "replication", "filesystem", map[string]any{"path": "/tmp/legacy"})

	_, r := storageRoutes(t, db)

	list := decodeJSONMap(t, r.do("GET", "/api/v1/admin/storage", ""))
	if items, _ := list["items"].([]any); len(items) != 1 {
		t.Fatalf("the record is not listed: %v", list)
	}

	// The status endpoint answers instead of the record.
	if got := decodeJSONMap(t, r.do("GET", storagePrefix+"replication", "")); got["name"] != nil {
		t.Errorf("GET replication returned the record, expected the replication status: %v", got)
	}

	rw := r.do("PATCH", storagePrefix+"replication", `{"name":"legacy"}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("renaming the record by name: expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	if rw = r.do("GET", storagePrefix+"legacy", ""); rw.Code != http.StatusOK {
		t.Errorf("the renamed record is not reachable: %d %s", rw.Code, rw.Body.String())
	}

	seedStorageBackend(t, db, "replication", "filesystem", map[string]any{"path": "/tmp/legacy2"})

	if rw = r.do("DELETE", storagePrefix+"replication", ""); rw.Code != http.StatusNoContent {
		t.Errorf("deleting the record by name: expected 204, got %d: %s", rw.Code, rw.Body.String())
	}
}

// The same for a record named "primary": the static routes answer GET and the
// test action, PATCH and DELETE act on the record while one exists (and are the
// 405 otherwise, which the primary tests cover).
func TestAdminStorage_ExistingPrimaryRecordCanBeRenamedAndDeleted(t *testing.T) {
	db := newMockDatabase()
	seedStorageBackend(t, db, "primary", "filesystem", map[string]any{"path": "/tmp/legacy"})

	_, r := storageRoutes(t, db)

	if rw := r.do("PATCH", storagePrefix+"primary", `{"name":"legacy"}`); rw.Code != http.StatusOK {
		t.Fatalf("renaming the record by name: expected 200, got %d: %s", rw.Code, rw.Body.String())
	}

	if rw := r.do("GET", storagePrefix+"legacy", ""); rw.Code != http.StatusOK {
		t.Errorf("the renamed record is not reachable: %d %s", rw.Code, rw.Body.String())
	}

	if rw := r.do("PATCH", storagePrefix+"primary", `{"name":"x"}`); rw.Code != http.StatusMethodNotAllowed {
		t.Errorf("once renamed, PATCH primary is read-only again: got %d", rw.Code)
	}

	db2 := newMockDatabase()
	seedStorageBackend(t, db2, "primary", "filesystem", map[string]any{"path": "/tmp/legacy2"})

	_, r = storageRoutes(t, db2)

	if rw := r.do("DELETE", storagePrefix+"primary", ""); rw.Code != http.StatusNoContent {
		t.Errorf("deleting the record by name: expected 204, got %d: %s", rw.Code, rw.Body.String())
	}

	if rw := r.do("DELETE", storagePrefix+"primary", ""); rw.Code != http.StatusMethodNotAllowed {
		t.Errorf("after the delete, DELETE primary is read-only again: got %d", rw.Code)
	}
}
