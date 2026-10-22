// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/tiering"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	tierHotID  = "10000000-0000-0000-0000-000000000001"
	tierColdID = "10000000-0000-0000-0000-000000000002"
	cold1ID    = "20000000-0000-0000-0000-000000000001"
)

type fakeTieringDB struct {
	tiers    []*models.StorageTier
	policies []*models.StoragePolicy
	backends map[string]*models.StorageBackend
	blobsOn  map[string]int64
	moves    []*models.StorageTierMove
}

func newFakeTieringDB() *fakeTieringDB {
	return &fakeTieringDB{
		tiers: []*models.StorageTier{
			{ID: tierHotID, Name: "hot", Rank: 0},
			{ID: tierColdID, Name: "cold", BackendID: cold1ID, BackendName: "hdd", BackendKind: "filesystem", Rank: 100},
		},
		backends: map[string]*models.StorageBackend{
			"hdd":  {ID: cold1ID, Name: "hdd", Kind: "filesystem"},
			"s3-b": {ID: "20000000-0000-0000-0000-000000000002", Name: "s3-b", Kind: "s3"},
		},
		blobsOn: map[string]int64{},
	}
}

func (f *fakeTieringDB) ListStorageTiers(context.Context) ([]*models.StorageTier, error) {
	return slices.Clone(f.tiers), nil
}

func (f *fakeTieringDB) GetStorageTierByName(_ context.Context, name string) (*models.StorageTier, error) {
	for _, t := range f.tiers {
		if t.Name == name {
			c := *t

			return &c, nil
		}
	}

	return nil, nil
}

func (f *fakeTieringDB) CreateStorageTier(_ context.Context, t *models.StorageTier) error {
	for _, e := range f.tiers {
		if e.Name == t.Name {
			return &pgconn.PgError{Code: "23505", ConstraintName: "storage_tiers_name_key"}
		}

		if e.BackendID == t.BackendID {
			return &pgconn.PgError{Code: "23505", ConstraintName: "idx_storage_tiers_backend"}
		}
	}

	c := *t
	f.tiers = append(f.tiers, &c)

	return nil
}

func (f *fakeTieringDB) UpdateStorageTier(_ context.Context, t *models.StorageTier) error {
	for i, e := range f.tiers {
		if e.ID == t.ID {
			c := *t
			f.tiers[i] = &c
		}
	}

	return nil
}

func (f *fakeTieringDB) DeleteStorageTierIfUnused(_ context.Context, id string) (string, error) {
	for _, p := range f.policies {
		if p.TargetTierID == id || p.FromTierID == id {
			return "storage tier is still referenced by a policy", nil
		}
	}

	for _, t := range f.tiers {
		if t.ID == id && f.blobsOn[t.BackendID] > 0 {
			return "storage tier still holds blobs", nil
		}
	}

	f.tiers = slices.DeleteFunc(f.tiers, func(t *models.StorageTier) bool { return t.ID == id })

	return "", nil
}

func (f *fakeTieringDB) GetStorageBackend(_ context.Context, name string) (*models.StorageBackend, error) {
	return f.backends[name], nil
}

func (f *fakeTieringDB) ListStoragePolicies(context.Context) ([]*models.StoragePolicy, error) {
	return slices.Clone(f.policies), nil
}

func (f *fakeTieringDB) GetStoragePolicy(_ context.Context, id string) (*models.StoragePolicy, error) {
	for _, p := range f.policies {
		if p.ID == id {
			c := *p

			return &c, nil
		}
	}

	return nil, nil
}

func (f *fakeTieringDB) CreateStoragePolicy(_ context.Context, p *models.StoragePolicy) error {
	for _, e := range f.policies {
		if e.Name == p.Name {
			return &pgconn.PgError{Code: "23505", ConstraintName: "storage_policies_name_key"}
		}
	}

	c := *p
	f.policies = append(f.policies, &c)

	return nil
}

func (f *fakeTieringDB) UpdateStoragePolicy(_ context.Context, p *models.StoragePolicy) error {
	for i, e := range f.policies {
		if e.ID == p.ID {
			c := *p
			f.policies[i] = &c
		}
	}

	return nil
}

func (f *fakeTieringDB) DeleteStoragePolicy(_ context.Context, id string) (bool, error) {
	n := len(f.policies)
	f.policies = slices.DeleteFunc(f.policies, func(p *models.StoragePolicy) bool { return p.ID == id })

	return len(f.policies) < n, nil
}

func (f *fakeTieringDB) StorageTierUsage(context.Context) ([]models.TierUsage, error) {
	return []models.TierUsage{{BackendID: "", Blobs: 7, Bytes: 700}, {BackendID: cold1ID, Blobs: 3, Bytes: 300}}, nil
}

func (f *fakeTieringDB) ListStorageTierMoves(context.Context, int) ([]*models.StorageTierMove, error) {
	return f.moves, nil
}

func (f *fakeTieringDB) CountStorageTierMoves(context.Context) (int64, error) {
	return int64(len(f.moves)), nil
}

type fakeTierPlanner struct {
	draft *tiering.Policy
	limit int
	calls int
	plan  *tiering.Plan
}

func (p *fakeTierPlanner) Plan(_ context.Context, draft *tiering.Policy, limit int) (*tiering.Plan, error) {
	p.draft, p.limit = draft, limit
	p.calls++

	return p.plan, nil
}

type fakeTierRunner struct {
	enabled   bool
	triggered int
	status    tiering.RunnerStatus
}

func (f *fakeTierRunner) Trigger() bool { f.triggered++; return true }
func (f *fakeTierRunner) Status() tiering.RunnerStatus {
	s := f.status
	s.Enabled = f.enabled

	return s
}

type fakeTierRegistry struct{ refreshed int }

func (f *fakeTierRegistry) Refresh(context.Context) error { f.refreshed++; return nil }
func (f *fakeTierRegistry) Snapshot() *tiering.Snapshot   { return &tiering.Snapshot{} }

type tieringRig struct {
	db      *fakeTieringDB
	planner *fakeTierPlanner
	runner  *fakeTierRunner
	reg     *fakeTierRegistry
	router  http.Handler
}

func newTieringRig(t *testing.T) *tieringRig {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	rig := &tieringRig{
		db:      newFakeTieringDB(),
		planner: &fakeTierPlanner{plan: &tiering.Plan{}},
		runner:  &fakeTierRunner{enabled: true},
		reg:     &fakeTierRegistry{},
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &auth.Principal{IsLegacy: true} // legacy key: holds every permission
			if req.Header.Get("X-Test-Anon") != "" {
				p = &auth.Principal{IsAnonymous: true}
			}

			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), p)))
		})
	})
	NewAdminTieringHandler(rig.db, rig.reg, rig.planner, rig.runner, log, nil).Register(r)
	rig.router = r

	return rig
}

func (rig *tieringRig) do(method, path, body string, hdr ...string) (int, map[string]any) {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}

	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)

	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	return rec.Code, out
}

func tierErrorCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		s, _ := e["code"].(string)

		return s
	}

	s, _ := body["code"].(string)

	return s
}

func TestTieringAdminRequiresAdmin(t *testing.T) {
	t.Parallel()

	rig := newTieringRig(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/tiering/tiers"},
		{http.MethodPost, "/api/v1/admin/tiering/tiers"},
		{http.MethodGet, "/api/v1/admin/tiering/policies"},
		{http.MethodPost, "/api/v1/admin/tiering/dry-run"},
		{http.MethodPost, "/api/v1/admin/tiering/run"},
		{http.MethodGet, "/api/v1/admin/tiering/status"},
	} {
		if code, _ := rig.do(tc.method, tc.path, "{}", "X-Test-Anon", "1"); code != http.StatusForbidden {
			t.Errorf("%s %s as anonymous = %d, want 403", tc.method, tc.path, code)
		}
	}

	if rig.planner.calls != 0 || rig.runner.triggered != 0 {
		t.Error("a forbidden request reached the planner or the runner")
	}
}

func TestTieringListShowsPlacementStats(t *testing.T) {
	t.Parallel()

	rig := newTieringRig(t)

	code, body := rig.do(http.MethodGet, "/api/v1/admin/tiering/tiers", "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}

	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}

	hot, _ := items[0].(map[string]any)
	cold, _ := items[1].(map[string]any)

	if hot["primary"] != true || hot["blob_count"] != float64(7) || hot["total_bytes"] != float64(700) {
		t.Errorf("hot tier = %v", hot)
	}

	if cold["primary"] != false || cold["backend"] != "hdd" || cold["blob_count"] != float64(3) || cold["total_bytes"] != float64(300) {
		t.Errorf("cold tier = %v", cold)
	}
}

func TestTieringCreateAndDeleteTier(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		setup  func(db *fakeTieringDB)
		want   int
		code   string
	}{
		{"create ok", http.MethodPost, "/tiers", `{"name":"archive","backend":"s3-b"}`, nil, http.StatusCreated, ""},
		{"create unknown backend", http.MethodPost, "/tiers", `{"name":"archive","backend":"nope"}`, nil, http.StatusBadRequest, "validation_failed"},
		{"create bad name", http.MethodPost, "/tiers", `{"name":"no spaces","backend":"s3-b"}`, nil, http.StatusBadRequest, "validation_failed"},
		{"create bad rank", http.MethodPost, "/tiers", `{"name":"a","backend":"s3-b","rank":-1}`, nil, http.StatusBadRequest, "validation_failed"},
		{"create duplicate backend", http.MethodPost, "/tiers", `{"name":"again","backend":"hdd"}`, nil, http.StatusConflict, "conflict"},
		{"create duplicate name", http.MethodPost, "/tiers", `{"name":"cold","backend":"s3-b"}`, nil, http.StatusConflict, "conflict"},
		{"create name over 64 chars", http.MethodPost, "/tiers", `{"name":"` + strings.Repeat("a", 65) + `","backend":"s3-b"}`, nil, http.StatusBadRequest, "validation_failed"},
		{"create bad json", http.MethodPost, "/tiers", `{`, nil, http.StatusBadRequest, "bad_request"},
		{"delete primary", http.MethodDelete, "/tiers/hot", "", nil, http.StatusBadRequest, "cannot_delete_primary"},
		{"delete unknown", http.MethodDelete, "/tiers/ghost", "", nil, http.StatusNotFound, "not_found"},
		{"delete empty tier", http.MethodDelete, "/tiers/cold", "", nil, http.StatusNoContent, ""},
		{"delete tier with blobs", http.MethodDelete, "/tiers/cold", "", func(db *fakeTieringDB) { db.blobsOn[cold1ID] = 2 }, http.StatusConflict, "conflict"},
		{"delete tier used by policy", http.MethodDelete, "/tiers/cold", "", func(db *fakeTieringDB) {
			db.policies = []*models.StoragePolicy{{ID: "p", Name: "p", TargetTierID: tierColdID}}
		}, http.StatusConflict, "conflict"},
		{"primary cannot promote on read", http.MethodPatch, "/tiers/hot", `{"promote_on_read":true}`, nil, http.StatusBadRequest, "validation_failed"},
		{"cold can promote on read", http.MethodPatch, "/tiers/cold", `{"promote_on_read":true}`, nil, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newTieringRig(t)
			if tc.setup != nil {
				tc.setup(rig.db)
			}

			code, body := rig.do(tc.method, "/api/v1/admin/tiering"+tc.path, tc.body)
			if code != tc.want || (tc.code != "" && tierErrorCode(body) != tc.code) {
				t.Fatalf("%s %s = %d %v; want %d %q", tc.method, tc.path, code, body, tc.want, tc.code)
			}

			if code < 300 && tc.method != http.MethodGet && rig.reg.refreshed == 0 {
				t.Error("registry was not refreshed after a tier change")
			}
		})
	}
}

func TestTieringPolicyCreateValidatesAndRoundTrips(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"idle policy", `{"name":"cooldown","target_tier":"cold","min_idle_seconds":2592000}`, http.StatusCreated},
		{"release glob", `{"name":"rel","target_tier":"cold","min_age_seconds":7776000,"repo_glob":"releases*","formats":["maven"],"path_glob":"com/**"}`, http.StatusCreated},
		{"no condition", `{"name":"all","target_tier":"cold"}`, http.StatusBadRequest},
		{"unknown target", `{"name":"x","target_tier":"nowhere","min_age_seconds":1}`, http.StatusBadRequest},
		{"unknown from", `{"name":"x","target_tier":"cold","from_tier":"nowhere","min_age_seconds":1}`, http.StatusBadRequest},
		{"from equals target", `{"name":"x","target_tier":"cold","from_tier":"cold","min_age_seconds":1}`, http.StatusBadRequest},
		{"negative age", `{"name":"x","target_tier":"cold","min_age_seconds":-5}`, http.StatusBadRequest},
		{"min above max size", `{"name":"x","target_tier":"cold","min_size_bytes":10,"max_size_bytes":5}`, http.StatusBadRequest},
		{"bad format", `{"name":"x","target_tier":"cold","formats":["Maven!"]}`, http.StatusBadRequest},
		{"bad name", `{"name":"","target_tier":"cold","min_age_seconds":1}`, http.StatusBadRequest},
		{"bad position", `{"name":"x","target_tier":"cold","min_age_seconds":1,"position":-1}`, http.StatusBadRequest},
		{"unknown field is ignored not fatal", `{"name":"x","target_tier":"cold","min_age_seconds":1,"bogus":1}`, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newTieringRig(t)

			code, body := rig.do(http.MethodPost, "/api/v1/admin/tiering/policies", tc.body)
			if code != tc.want {
				t.Fatalf("status = %d %v, want %d", code, body, tc.want)
			}

			if code != http.StatusCreated {
				if len(rig.db.policies) != 0 {
					t.Error("a rejected policy was stored")
				}

				return
			}

			if body["target_tier"] != "cold" || body["enabled"] != true {
				t.Errorf("response = %v", body)
			}
		})
	}
}

func TestTieringPolicyReplaceListDelete(t *testing.T) {
	t.Parallel()

	rig := newTieringRig(t)

	code, created := rig.do(http.MethodPost, "/api/v1/admin/tiering/policies", `{"name":"p","target_tier":"cold","min_age_seconds":86400}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, created)
	}

	// The fake store keeps whatever id the handler generated.
	id, _ := created["id"].(string)
	url := "/api/v1/admin/tiering/policies/" + id

	// Duplicate name conflicts.
	if code, _ := rig.do(http.MethodPost, "/api/v1/admin/tiering/policies", `{"name":"p","target_tier":"cold","min_age_seconds":1}`); code != http.StatusConflict {
		t.Errorf("duplicate name = %d, want 409", code)
	}

	// PUT replaces: omitted conditions disappear.
	code, got := rig.do(http.MethodPut, url, `{"name":"p","enabled":false,"target_tier":"cold","min_size_bytes":1048576}`)
	if code != http.StatusOK || got["enabled"] != false || got["min_size_bytes"] != float64(1048576) {
		t.Fatalf("replace = %d %v", code, got)
	}

	if got["min_age_seconds"] != nil {
		t.Errorf("min_age_seconds survived a full replace: %v", got["min_age_seconds"])
	}

	if code, list := rig.do(http.MethodGet, "/api/v1/admin/tiering/policies", ""); code != http.StatusOK || list["total"] != float64(1) {
		t.Errorf("list = %d %v", code, list)
	}

	if code, _ := rig.do(http.MethodPut, "/api/v1/admin/tiering/policies/30000000-0000-0000-0000-000000000000", `{"name":"q","target_tier":"cold","min_age_seconds":1}`); code != http.StatusNotFound {
		t.Errorf("replace unknown = %d, want 404", code)
	}

	if code, _ := rig.do(http.MethodGet, "/api/v1/admin/tiering/policies/not-a-uuid", ""); code != http.StatusNotFound {
		t.Errorf("malformed id = %d, want 404", code)
	}

	if code, _ := rig.do(http.MethodDelete, url, ""); code != http.StatusNoContent {
		t.Errorf("delete = %d", code)
	}

	if code, _ := rig.do(http.MethodDelete, url, ""); code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", code)
	}
}

func TestTieringDryRun(t *testing.T) {
	t.Parallel()

	rig := newTieringRig(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rig.planner.plan = &tiering.Plan{
		Scanned: 10, Count: 2, Bytes: 300,
		Routes:   []tiering.PlanRoute{{From: "hot", To: "cold", Count: 2, Bytes: 300}},
		Policies: []tiering.PlanPolicy{{Policy: "draft", Count: 2, Bytes: 300}},
		Sample:   []tiering.PlanSample{{SHA256: strings.Repeat("a", 64), SizeBytes: 100, From: "hot", To: "cold", Policy: "draft", CreatedAt: created}},
	}

	code, body := rig.do(http.MethodPost, "/api/v1/admin/tiering/dry-run",
		`{"policy":{"target_tier":"cold","min_age_seconds":3600},"sample_limit":5}`)
	if code != http.StatusOK {
		t.Fatalf("status %d %v", code, body)
	}

	if rig.planner.draft == nil || rig.planner.draft.Name != "draft" || rig.planner.draft.TargetTierID != tierColdID || rig.planner.limit != 5 {
		t.Errorf("planner got draft=%+v limit=%d", rig.planner.draft, rig.planner.limit)
	}

	wm, _ := body["would_move"].(map[string]any)
	if wm["count"] != float64(2) || wm["bytes"] != float64(300) || body["scanned"] != float64(10) {
		t.Errorf("body = %v", body)
	}

	if s, _ := body["sample"].([]any); len(s) != 1 {
		t.Errorf("sample = %v", body["sample"])
	}

	// No draft: every saved policy is simulated.
	rig.do(http.MethodPost, "/api/v1/admin/tiering/dry-run", `{}`)

	if rig.planner.draft != nil {
		t.Errorf("empty body produced a draft: %+v", rig.planner.draft)
	}

	for _, bad := range []string{`{"sample_limit":201}`, `{"sample_limit":-1}`, `{"policy":{"target_tier":"cold"}}`, `{`} {
		before := rig.planner.calls

		if code, _ := rig.do(http.MethodPost, "/api/v1/admin/tiering/dry-run", bad); code != http.StatusBadRequest {
			t.Errorf("dry-run %s = %d, want 400", bad, code)
		}

		if rig.planner.calls != before {
			t.Errorf("planner ran for the invalid request %s", bad)
		}
	}
}

func TestTieringRunAndStatus(t *testing.T) {
	t.Parallel()

	rig := newTieringRig(t)
	rig.db.moves = []*models.StorageTierMove{{SHA256: strings.Repeat("b", 64), FromBackendID: "", ToBackendID: cold1ID, State: models.TierMoveCopying, Attempts: 2}}

	code, body := rig.do(http.MethodPost, "/api/v1/admin/tiering/run", "")
	if code != http.StatusAccepted || body["started"] != true || rig.runner.triggered != 1 {
		t.Fatalf("run = %d %v (triggered %d)", code, body, rig.runner.triggered)
	}

	code, body = rig.do(http.MethodGet, "/api/v1/admin/tiering/status", "")
	if code != http.StatusOK || body["enabled"] != true || body["pending_total"] != float64(1) {
		t.Fatalf("status = %d %v", code, body)
	}

	rig.runner.enabled = false

	if code, _ = rig.do(http.MethodPost, "/api/v1/admin/tiering/run", ""); code != http.StatusConflict || rig.runner.triggered != 1 {
		t.Errorf("run with a disabled mover = %d (triggered %d), want 409 and no trigger", code, rig.runner.triggered)
	}
}
