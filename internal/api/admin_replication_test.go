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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/replication"
	"github.com/openartifact/openartifact/pkg/logger"
)

type fakeReplication struct {
	status    replication.Status
	statusErr error
	// accept is what RequestVerify answers.
	accept   bool
	verifyEr error
	requests int
}

func (f *fakeReplication) Status(context.Context) (replication.Status, error) {
	return f.status, f.statusErr
}

func (f *fakeReplication) RequestVerify(context.Context) (bool, error) {
	f.requests++

	return f.accept, f.verifyEr
}

func replicationRouter(t *testing.T, svc ReplicationService) http.Handler {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &auth.Principal{IsLegacy: true}
			if req.Header.Get("X-Test-Anon") != "" {
				p = &auth.Principal{IsAnonymous: true}
			}

			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), p)))
		})
	})

	// The parameterized storage backend route the replication routes must win against.
	r.Get("/api/v1/admin/storage/{name}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	NewAdminReplicationHandler(svc, log, nil).Register(r)

	return r
}

func doReplication(h http.Handler, method, path string, hdr ...string) (int, map[string]any) {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	return rec.Code, out
}

func TestReplicationAdminRequiresAdmin(t *testing.T) {
	t.Parallel()

	h := replicationRouter(t, &fakeReplication{accept: true})

	for _, ep := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/storage/replication"},
		{http.MethodPost, "/api/v1/admin/storage/replication/verify"},
	} {
		if code, _ := doReplication(h, ep.method, ep.path, "X-Test-Anon", "1"); code != http.StatusForbidden {
			t.Errorf("%s %s as anonymous = %d, want 403", ep.method, ep.path, code)
		}
	}
}

func TestReplicationStatus(t *testing.T) {
	t.Parallel()

	finished := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	errAt := finished.Add(time.Hour)

	svc := &fakeReplication{status: replication.Status{
		Enabled: true, Replica: replication.ReplicaInfo{Type: "s3", Location: "s3.example.com/dr"}, ReadFallback: "notfound",
		BacklogCopy: 3, BacklogDelete: 1, OldestPendingSeconds: 12.5, Failing: 1,
		ReplicatedTotal: 40, ReplicatedBytes: 4096, LastError: "replica down", LastErrorAt: &errAt,
		LastVerify:    &replication.VerifyResult{StartedAt: finished.Add(-time.Minute), FinishedAt: &finished, Checked: 10, MissingReplica: 2, Repaired: 2, ReplicaListed: true, Listing: "list"},
		VerifyRunning: &replication.VerifyResult{StartedAt: errAt, Checked: 1},
	}}

	code, body := doReplication(replicationRouter(t, svc), http.MethodGet, "/api/v1/admin/storage/replication")
	if code != http.StatusOK {
		t.Fatalf("status = %d (%v)", code, body)
	}

	replica, _ := body["replica"].(map[string]any)
	last, _ := body["last_verify"].(map[string]any)
	running, _ := body["verify_running"].(map[string]any)

	checks := map[string]bool{
		"enabled":      body["enabled"] == true,
		"replica":      replica["type"] == "s3" && replica["location"] == "s3.example.com/dr",
		"fallback":     body["read_fallback"] == "notfound",
		"backlog":      body["backlog_copy"] == float64(3) && body["backlog_delete"] == float64(1),
		"lag":          body["oldest_pending_age_seconds"] == 12.5,
		"failing":      body["failing"] == float64(1),
		"totals":       body["replicated_total"] == float64(40) && body["replicated_bytes_total"] == float64(4096),
		"last error":   body["last_error"] == "replica down" && body["last_error_at"] != nil,
		"last verify":  last["checked"] == float64(10) && last["missing_replica"] == float64(2) && last["finished_at"] != nil,
		"verify run":   running["checked"] == float64(1) && running["finished_at"] == nil,
		"not as value": body["verify_requested"] == false,
	}

	for name, ok := range checks {
		if !ok {
			t.Errorf("%s wrong in %v", name, body)
		}
	}
}

func TestReplicationStatusNotConfigured(t *testing.T) {
	t.Parallel()

	code, body := doReplication(replicationRouter(t, nil), http.MethodGet, "/api/v1/admin/storage/replication")
	if code != http.StatusOK || body["enabled"] != false || body["replica"] != nil {
		t.Fatalf("status = %d %v", code, body)
	}
}

func TestReplicationStatusFailureDoesNotLeak(t *testing.T) {
	t.Parallel()

	svc := &fakeReplication{statusErr: errors.New("pq: password authentication failed for user oa")}

	code, body := doReplication(replicationRouter(t, svc), http.MethodGet, "/api/v1/admin/storage/replication")
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d", code)
	}

	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "pq:") {
		t.Fatalf("internal error leaked: %s", raw)
	}
}

func TestReplicationVerifyRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		svc      ReplicationService
		wantCode int
		wantReqs int
	}{
		{"accepted", &fakeReplication{accept: true}, http.StatusAccepted, 1},
		{"already running", &fakeReplication{accept: false}, http.StatusConflict, 1},
		{"not configured", nil, http.StatusNotFound, 0},
		{"database error", &fakeReplication{verifyEr: errors.New("boom")}, http.StatusInternalServerError, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			code, body := doReplication(replicationRouter(t, tc.svc), http.MethodPost, "/api/v1/admin/storage/replication/verify")
			if code != tc.wantCode {
				t.Fatalf("status = %d (%v), want %d", code, body, tc.wantCode)
			}

			if f, ok := tc.svc.(*fakeReplication); ok && f.requests != tc.wantReqs {
				t.Fatalf("requests = %d", f.requests)
			}

			if tc.wantCode == http.StatusAccepted && body["requested"] != true {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

func TestReplicationRoutesWinOverStorageBackendName(t *testing.T) {
	t.Parallel()

	h := replicationRouter(t, &fakeReplication{})

	if code, _ := doReplication(h, http.MethodGet, "/api/v1/admin/storage/replication"); code == http.StatusTeapot {
		t.Fatal("the storage backend route shadowed the replication status")
	}

	if code, _ := doReplication(h, http.MethodGet, "/api/v1/admin/storage/other"); code != http.StatusTeapot {
		t.Fatalf("other backends are no longer reachable: %d", code)
	}
}
