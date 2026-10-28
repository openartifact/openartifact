// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/exploit"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	secProjectA  = "proj-a" // the caller maintains it
	secProjectB  = "proj-b" // the caller is a developer there
	secProjectC  = "proj-c" // the caller owns it
	secFindingID = "9a000000-0000-4000-8000-000000000001"
)

// securityMockDB serves the findings, summary, health and repository summary
// queries and records what the handlers asked.
type securityMockDB struct {
	databasetest.BaseMockDB

	admins   map[string]bool
	grants   map[string][]*models.ProjectGrant // by user ID
	repos    map[string]*models.Repository
	settings map[string]string

	pages      []*models.FindingPage // served in order, one per call
	summary    *models.FindingsSummary
	stats      *models.SecurityHealthStats
	push       *models.DependencyTrackLastPush
	repoSum    *models.RepoSecuritySummary
	err        error
	vulnScans  map[string]*models.VulnScanResult // by artifact id
	properties map[string]string

	listCalls     []models.FindingListParams
	summaryCalls  []models.FindingFilter
	statsCalls    int
	repoSumCalls  []string
	repoSumRepoID string
}

var _ database.Database = (*securityMockDB)(nil)

func (db *securityMockDB) GetPermissionTargetsForPrincipal(
	_ context.Context,
	userID string,
	_ []string,
) ([]*models.PermissionTarget, error) {
	if db.admins[userID] {
		return adminTargets(), nil
	}

	return nil, nil
}

func (db *securityMockDB) ListProjectGrantsByUser(_ context.Context, userID string) ([]*models.ProjectGrant, error) {
	return db.grants[userID], nil
}

func (db *securityMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return db.repos[name], nil
}

func (db *securityMockDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	return db.settings[key], nil
}

func (db *securityMockDB) ListSecurityFindings(_ context.Context, p models.FindingListParams) (*models.FindingPage, error) {
	db.listCalls = append(db.listCalls, p)

	if db.err != nil {
		return nil, db.err
	}

	if len(db.pages) == 0 {
		return &models.FindingPage{}, nil
	}

	page := db.pages[0]
	if len(db.pages) > 1 {
		db.pages = db.pages[1:]
	}

	return page, nil
}

func (db *securityMockDB) SecurityFindingsSummary(_ context.Context, f models.FindingFilter) (*models.FindingsSummary, error) {
	db.summaryCalls = append(db.summaryCalls, f)

	return db.summary, db.err
}

func (db *securityMockDB) SecurityHealthStats(context.Context) (*models.SecurityHealthStats, error) {
	db.statsCalls++

	return db.stats, db.err
}

func (db *securityMockDB) LatestFinishedDependencyTrackPush(context.Context) (*models.DependencyTrackLastPush, error) {
	return db.push, db.err
}

func (db *securityMockDB) RepoSecuritySummary(_ context.Context, repoID, pkg, version string) (*models.RepoSecuritySummary, error) {
	db.repoSumCalls = append(db.repoSumCalls, pkg+"@"+version)
	db.repoSumRepoID = repoID

	return db.repoSum, db.err
}

// The VEX export reads these through the SBOM generator.
func (db *securityMockDB) ListArtifactProperties(context.Context, string, string) (map[string]string, error) {
	return db.properties, nil
}

func (db *securityMockDB) GetLicenseScanResult(context.Context, string, string) (*models.LicenseScanResult, error) {
	return nil, nil
}

func (db *securityMockDB) GetVulnScanResult(_ context.Context, _, artifactID string) (*models.VulnScanResult, error) {
	return db.vulnScans[artifactID], nil
}

func (db *securityMockDB) ListVEXStatements(context.Context, string, string) ([]*models.VEXStatement, error) {
	return nil, nil
}

// securityCaller is a principal of the tests.
type securityCaller struct {
	name      string
	principal *auth.Principal
	// wantScope is the scope the handler must pass to the database; nil: the caller gets 403.
	wantScope *models.FindingScope
	admin     bool
}

func securityCallers() []securityCaller {
	return []securityCaller{
		{"global admin", &auth.Principal{UserID: new(uAdmin1), Username: "admin-1"}, &models.FindingScope{All: true}, true},
		{"legacy admin key", &auth.Principal{Username: "admin", IsLegacy: true}, &models.FindingScope{All: true}, true},
		{"maintainer and owner", &auth.Principal{UserID: new(uMaint1), Username: "maint-1"},
			&models.FindingScope{ProjectIDs: []string{secProjectA, secProjectC}}, false},
		{"developer only", &auth.Principal{UserID: new(uDev1), Username: "dev-1"}, nil, false},
		{"viewer only", &auth.Principal{UserID: new(uView1), Username: "view-1"}, nil, false},
		{"user without role", &auth.Principal{UserID: new(uPlain1), Username: "plain-1"}, nil, false},
		{"anonymous", &auth.Principal{IsAnonymous: true}, nil, false},
		{"no principal", nil, nil, false},
		{
			"scoped token of a maintainer",
			&auth.Principal{UserID: new(uMaint1), TokenKind: auth.TokenKindAccess, TokenScopes: []string{"t-1"}},
			nil,
			false,
		},
		{"deploy token of a maintainer", &auth.Principal{UserID: new(uMaint1), TokenKind: auth.TokenKindDeploy}, nil, false},
		{"deploy token of an admin", &auth.Principal{UserID: new(uAdmin1), TokenKind: auth.TokenKindDeploy}, nil, false},
		{"project token service account", &auth.Principal{UserID: new(uMaint1), ServiceProjectID: secProjectA}, nil, false},
	}
}

func newSecurityMockDB() *securityMockDB {
	return &securityMockDB{
		admins: map[string]bool{uAdmin1: true},
		grants: map[string][]*models.ProjectGrant{
			uMaint1: {
				{ProjectID: secProjectA, Role: models.ProjectRoleMaintainer},
				{ProjectID: secProjectB, Role: models.ProjectRoleDeveloper},
				{ProjectID: secProjectC, Role: models.ProjectRoleOwner},
			},
			uDev1:  {{ProjectID: secProjectA, Role: models.ProjectRoleDeveloper}},
			uView1: {{ProjectID: secProjectA, Role: models.ProjectRoleViewer}},
		},
		repos:    map[string]*models.Repository{},
		settings: map[string]string{},
	}
}

func serveSecurity(t *testing.T, db *securityMockDB, principal *auth.Principal, target string) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	h := NewSecurityHandler(db, log, auth.NewEvaluator(db, log, false), nil, sbom.NewGenerator(db, log), ai.Settings{})

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal != nil {
				r = r.WithContext(middleware.WithPrincipal(r.Context(), principal))
			}

			next.ServeHTTP(w, r)
		})
	})
	h.RegisterRoutes(router)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func decodeBody(t *testing.T, rw *httptest.ResponseRecorder, v any) {
	t.Helper()

	if err := json.Unmarshal(rw.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rw.Body.String(), err)
	}
}

// TestSecurityFindings_Scope: global admins see everything, project maintainers
// and owners the repositories of their projects, everyone else gets 403 on the
// list, the summary and the export without the database being queried.
func TestSecurityFindings_Scope(t *testing.T) {
	for _, target := range []string{
		"/api/v1/security/findings",
		"/api/v1/security/findings/summary",
		"/api/v1/security/findings/export?format=csv",
	} {
		for _, c := range securityCallers() {
			t.Run(c.name+" "+target, func(t *testing.T) {
				db := newSecurityMockDB()
				db.summary = &models.FindingsSummary{ByType: map[string]models.FindingTypeSummary{}}

				rw := serveSecurity(t, db, c.principal, target)

				calls := append(slices.Clone(db.summaryCalls), make([]models.FindingFilter, len(db.listCalls))...)
				for i, p := range db.listCalls {
					calls[len(db.summaryCalls)+i] = p.FindingFilter
				}

				if c.wantScope == nil {
					if rw.Code != http.StatusForbidden {
						t.Fatalf("status = %d, want 403 (%s)", rw.Code, rw.Body.String())
					}

					if len(calls) != 0 {
						t.Fatal("the database was queried for a caller without a findings scope")
					}

					return
				}

				if rw.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (%s)", rw.Code, rw.Body.String())
				}

				if len(calls) == 0 {
					t.Fatal("no database query")
				}

				for _, f := range calls {
					if f.Scope.All != c.wantScope.All || !slices.Equal(f.Scope.ProjectIDs, c.wantScope.ProjectIDs) {
						t.Fatalf("scope = %+v, want %+v", f.Scope, *c.wantScope)
					}
				}
			})
		}
	}
}

func TestSecurityHealth_AdminOnly(t *testing.T) {
	for _, c := range securityCallers() {
		t.Run(c.name, func(t *testing.T) {
			db := newSecurityMockDB()
			db.stats = &models.SecurityHealthStats{}

			rw := serveSecurity(t, db, c.principal, "/api/v1/security/health")

			want := http.StatusForbidden
			if c.admin {
				want = http.StatusOK
			}

			if rw.Code != want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, want, rw.Body.String())
			}

			if !c.admin && db.statsCalls != 0 {
				t.Fatal("the database was queried for a non-admin")
			}
		})
	}
}

func TestSecurityHealth_Response(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	db := newSecurityMockDB()
	db.stats = &models.SecurityHealthStats{ScansPending: 3, ScansFailed24h: 2, LastScanAt: &at, UnscannedRepositories: 4}
	db.push = &models.DependencyTrackLastPush{OK: false, At: at}
	db.settings["dependency_track.global"] = `{"enabled":true}`
	db.settings["ai.enabled"] = "true"

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/health")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	var got map[string]any
	decodeBody(t, rw, &got)

	for _, key := range []string{"scanners", "last_scan_at", "scans_pending", "scans_failed_24h", "dependency_track", "ai", "unscanned_repositories"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %q in %s", key, rw.Body.String())
		}
	}

	if got["scans_pending"] != float64(3) || got["scans_failed_24h"] != float64(2) || got["unscanned_repositories"] != float64(4) {
		t.Errorf("counts = %s", rw.Body.String())
	}

	if got["last_scan_at"] != at.Format(time.RFC3339Nano) {
		t.Errorf("last_scan_at = %v", got["last_scan_at"])
	}

	dt := got["dependency_track"].(map[string]any)
	if dt["enabled"] != true || dt["last_push_ok"] != false || dt["last_push_at"] != at.Format(time.RFC3339Nano) {
		t.Errorf("dependency_track = %v", dt)
	}

	if got["ai"].(map[string]any)["enabled"] != true {
		t.Errorf("ai = %v", got["ai"])
	}
}

func TestSecurityHealth_NoPushAndNoScans(t *testing.T) {
	db := newSecurityMockDB()
	db.stats = &models.SecurityHealthStats{}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/health")

	var got map[string]any
	decodeBody(t, rw, &got)

	if v, ok := got["last_scan_at"]; !ok || v != nil {
		t.Errorf("last_scan_at = %v (present %v), want null", v, ok)
	}

	dt := got["dependency_track"].(map[string]any)
	if _, ok := dt["last_push_ok"]; ok {
		t.Errorf("last_push_ok must be absent without a push: %v", dt)
	}

	if _, ok := dt["last_push_at"]; ok {
		t.Errorf("last_push_at must be absent without a push: %v", dt)
	}

	if dt["enabled"] != false || got["ai"].(map[string]any)["enabled"] != false {
		t.Errorf("integrations must be off by default: %s", rw.Body.String())
	}

	if scanners, ok := got["scanners"].([]any); !ok || len(scanners) != 0 {
		t.Errorf("scanners = %v, want an empty list", got["scanners"])
	}
}

func TestSecurityFindings_FiltersReachTheQuery(t *testing.T) {
	all := models.FindingTypes()

	tests := []struct {
		query string
		want  models.FindingListParams
	}{
		{"", models.FindingListParams{Types: all, Limit: 50}},
		{"limit=7", models.FindingListParams{Types: all, Limit: 7}},
		{"limit=900", models.FindingListParams{Types: all, Limit: 200}},
		{"limit=0", models.FindingListParams{Types: all, Limit: 50}},
		{"type=vuln", models.FindingListParams{Types: []string{"vuln"}, Limit: 50}},
		{"type=ai,vuln,vuln", models.FindingListParams{Types: []string{"vuln", "ai"}, Limit: 50}},
		{"type=secret%2Clicense", models.FindingListParams{Types: []string{"license", "secret"}, Limit: 50}},
		{"repo=npm-main", models.FindingListParams{Repo: "npm-main", Types: all, Limit: 50}},
		{"project=acme", models.FindingListParams{Project: "acme", Types: all, Limit: 50}},
		{"severity=high", models.FindingListParams{Severity: "high", Types: all, Limit: 50}},
		{"status=suppressed", models.FindingListParams{Status: "suppressed", Types: all, Limit: 50}},
		{"q=%20lodash%20", models.FindingListParams{Q: "lodash", Types: all, Limit: 50}},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			db := newSecurityMockDB()

			rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings?"+tc.query)
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
			}

			got := db.listCalls[0]
			got.Scope = models.FindingScope{}

			want := tc.want
			if got.Repo != want.Repo || got.Project != want.Project || got.Severity != want.Severity || got.Status != want.Status ||
				got.Q != want.Q || got.Limit != want.Limit || !slices.Equal(got.Types, want.Types) || got.After != nil {
				t.Fatalf("params = %+v, want %+v", got, want)
			}
		})
	}
}

func TestSecurityFindings_ExploitParamsReachTheQuery(t *testing.T) {
	f, tr := false, true
	half, kevRank, lowRank := 0.5, 6, 1

	tests := []struct {
		query string
		check func(p models.FindingListParams) bool
	}{
		{"", func(p models.FindingListParams) bool {
			return p.Sort == "severity" && p.KEV == nil && p.EPSSMin == nil && p.PriorityRank == nil &&
				p.EPSSThreshold == exploit.DefaultEPSSThreshold
		}},
		{"sort=priority", func(p models.FindingListParams) bool { return p.Sort == "priority" }},
		{"sort=severity", func(p models.FindingListParams) bool { return p.Sort == "severity" }},
		{"kev=true", func(p models.FindingListParams) bool { return p.KEV != nil && *p.KEV == tr }},
		{"kev=false", func(p models.FindingListParams) bool { return p.KEV != nil && *p.KEV == f }},
		{"epss_min=0.5", func(p models.FindingListParams) bool { return p.EPSSMin != nil && *p.EPSSMin == half }},
		{"priority=kev", func(p models.FindingListParams) bool { return p.PriorityRank != nil && *p.PriorityRank == kevRank }},
		{"priority=low", func(p models.FindingListParams) bool { return p.PriorityRank != nil && *p.PriorityRank == lowRank }},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			db := newSecurityMockDB()

			if rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings?"+tc.query); rw.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
			}

			if got := db.listCalls[0]; !tc.check(got) {
				t.Fatalf("params = %+v", got)
			}
		})
	}
}

func TestSecurityFindings_CursorsAreBoundToTheirSort(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	for _, sort := range []string{"severity", "priority"} {
		other := map[string]string{"severity": "priority", "priority": "severity"}[sort]
		cursor := encodeFindingCursor(&models.FindingCursor{Sort: sort, Rank: 3, ScannedAt: at, ID: secFindingID})

		db := newSecurityMockDB()
		own := "/api/v1/security/findings?sort=" + sort + "&cursor=" + cursor
		if rw := serveSecurity(t, db, adminPrincipal(), own); rw.Code != http.StatusOK {
			t.Fatalf("%s cursor under its own sort: status = %d (%s)", sort, rw.Code, rw.Body.String())
		}

		if got := db.listCalls[0].After; got == nil || got.Sort != sort || got.Rank != 3 {
			t.Fatalf("%s cursor reached the query as %+v", sort, got)
		}

		for _, path := range []string{"/api/v1/security/findings", "/api/v1/security/findings/export"} {
			db = newSecurityMockDB()

			rw := serveSecurity(t, db, adminPrincipal(), path+"?sort="+other+"&cursor="+cursor)
			if rw.Code != http.StatusBadRequest || len(db.listCalls) != 0 {
				t.Fatalf("%s cursor under sort=%s on %s: status = %d, calls = %d", sort, other, path, rw.Code, len(db.listCalls))
			}
		}
	}

	// A cursor minted before sort existed is a severity cursor; the priority
	// ranks 5 and 6 only exist under the priority sort.
	legacy := base64.RawURLEncoding.EncodeToString([]byte("3:1:" + secFindingID))

	db := newSecurityMockDB()
	if rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings?cursor="+legacy); rw.Code != http.StatusOK {
		t.Fatalf("legacy cursor: status = %d", rw.Code)
	}

	for name, raw := range map[string]string{
		"severity rank 5":    "5:1:" + secFindingID,
		"priority rank 7":    "p:7:1:" + secFindingID,
		"priority too short": "p:3:1",
	} {
		rw := serveSecurity(t, newSecurityMockDB(), adminPrincipal(),
			"/api/v1/security/findings?sort=priority&cursor="+base64.RawURLEncoding.EncodeToString([]byte(raw)))
		if rw.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rw.Code)
		}
	}
}

func TestSecurityFindings_ExploitFieldsInResponseAndPaging(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	score := 0.42

	db := newSecurityMockDB()
	db.pages = []*models.FindingPage{{
		Findings: []*models.Finding{
			{ID: secFindingID, Type: "vuln", SeverityRank: 2, SortRank: 6, KEVCount: 2, MaxEPSS: &score, ScannedAt: at},
			{ID: "9a000000-0000-4000-8000-000000000002", Type: "vuln", SeverityRank: 4, SortRank: 5, MaxEPSS: &score, ScannedAt: at},
			{ID: "9a000000-0000-4000-8000-000000000003", Type: "vuln", SeverityRank: 4, SortRank: 4, ScannedAt: at},
			{ID: "9a000000-0000-4000-8000-000000000004", Type: "license", ScannedAt: at},
		},
		Next: &models.FindingCursor{Sort: "priority", Rank: 6, ScannedAt: at, ID: secFindingID},
	}}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings?sort=priority")

	var got struct {
		Findings   []map[string]any `json:"findings"`
		NextCursor string           `json:"next_cursor"`
	}

	decodeBody(t, rw, &got)

	wantPriority := []string{"kev", "epss", "critical", "none"}
	for i, f := range got.Findings {
		if f["priority"] != wantPriority[i] {
			t.Errorf("finding %d priority = %v, want %s", i, f["priority"], wantPriority[i])
		}
	}

	if got.Findings[0]["kev_count"] != float64(2) || got.Findings[0]["max_epss"] != score {
		t.Errorf("first = %v", got.Findings[0])
	}

	if v, ok := got.Findings[2]["max_epss"]; !ok || v != nil || got.Findings[2]["kev_count"] != float64(0) {
		t.Errorf("unscored finding = %v, want kev_count 0 and a null max_epss", got.Findings[2])
	}

	raw, err := base64.RawURLEncoding.DecodeString(got.NextCursor)
	if err != nil || !strings.HasPrefix(string(raw), "p:6:") {
		t.Fatalf("next cursor = %q (%v)", raw, err)
	}
}

func TestSecurityFindings_InvalidParams(t *testing.T) {
	long := strings.Repeat("x", maxNameLen+1)

	for _, q := range []string{
		"type=vuln,nope", "type=", "type=,", "severity=urgent", "severity=none", "status=gone", "repo=" + long, "project=" + long,
		"q=" + strings.Repeat("x", securityQueryMaxLen+1), "cursor=!!!",
		"sort=newest", "sort=PRIORITY", "kev=maybe", "kev=1", "epss_min=abc", "epss_min=-0.1", "epss_min=1.5", "epss_min=NaN",
		"priority=none", "priority=urgent", "priority=KEV",
	} {
		for _, path := range []string{"/api/v1/security/findings", "/api/v1/security/findings/export"} {
			t.Run(path+"?"+q[:min(len(q), 24)], func(t *testing.T) {
				db := newSecurityMockDB()

				rw := serveSecurity(t, db, adminPrincipal(), path+"?"+q)
				if q == "type=" {
					// an empty type means the default
					if rw.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200 for an empty type", rw.Code)
					}

					return
				}

				if rw.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (%s)", rw.Code, rw.Body.String())
				}

				if len(db.listCalls) != 0 {
					t.Fatal("the database was queried for an invalid request")
				}
			})
		}
	}
}

func TestSecurityFindings_ForgedCursorsAreRejected(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	for name, cursor := range map[string]string{
		"not base64":        "!!!",
		"too few parts":     enc("3:1"),
		"too many parts":    enc("3:1:" + secFindingID + ":x"),
		"rank out of range": enc("9:1:" + secFindingID),
		"negative rank":     enc("-1:1:" + secFindingID),
		"non numeric time":  enc("3:abc:" + secFindingID),
		"time overflow":     enc("3:9223372036854775807:" + secFindingID),
		"id not a uuid":     enc("3:1:'; DROP TABLE x"),
	} {
		t.Run(name, func(t *testing.T) {
			db := newSecurityMockDB()

			rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings?cursor="+cursor)
			if rw.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rw.Code, rw.Body.String())
			}

			if len(db.listCalls) != 0 {
				t.Fatal("a forged cursor reached the database")
			}
		})
	}
}

func TestSecurityFindings_CursorRoundTripAndPaging(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 123456000, time.UTC)
	next := &models.FindingCursor{Sort: "severity", Rank: 3, ScannedAt: at, ID: secFindingID}

	db := newSecurityMockDB()
	db.pages = []*models.FindingPage{
		{Findings: []*models.Finding{{ID: secFindingID, Type: "vuln", SeverityRank: 3}}, Next: next},
		{Findings: []*models.Finding{{ID: "9a000000-0000-4000-8000-000000000002", Type: "ai", SeverityRank: 1}}},
	}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings?limit=1")

	var first struct {
		Findings   []map[string]any `json:"findings"`
		HasMore    bool             `json:"has_more"`
		NextCursor string           `json:"next_cursor"`
	}

	decodeBody(t, rw, &first)

	if !first.HasMore || first.NextCursor == "" || len(first.Findings) != 1 {
		t.Fatalf("first page = %s", rw.Body.String())
	}

	rw = serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings?limit=1&cursor="+first.NextCursor)

	var second struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	}

	decodeBody(t, rw, &second)

	if second.HasMore || second.NextCursor != "" {
		t.Fatalf("last page = %s", rw.Body.String())
	}

	got := db.listCalls[1].After
	if got == nil || got.Sort != "severity" || got.Rank != 3 || !got.ScannedAt.Equal(at) || got.ID != secFindingID {
		t.Fatalf("cursor reached the query as %+v, want %+v", got, next)
	}
}

func TestSecurityFindings_ResponseShape(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	db := newSecurityMockDB()
	db.pages = []*models.FindingPage{{Findings: []*models.Finding{
		{
			ID: secFindingID, Type: "vuln", Repository: "npm-main", Format: "npm", ArtifactID: "lodash@4.0.0",
			PackageName: "lodash", PackageVersion: "4.0.0", Scanner: "osv", Critical: 1, High: 2, Status: "open",
			VEXStatus: "affected", SeverityRank: 4, Title: "CVE-2026-1", ScannedAt: at,
		},
		{
			ID:          "9a000000-0000-4000-8000-000000000002",
			Type:        "license",
			Repository:  "r",
			Status:      "quarantined",
			Quarantined: true,
			ScannedAt:   at,
		},
	}}}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings")

	var got struct {
		Findings []map[string]any `json:"findings"`
		HasMore  bool             `json:"has_more"`
	}

	decodeBody(t, rw, &got)

	if len(got.Findings) != 2 || got.HasMore {
		t.Fatalf("body = %s", rw.Body.String())
	}

	wantKeys := []string{
		"artifact_id", "counts", "format", "id", "kev_count", "max_epss", "package_name", "package_version", "priority",
		"quarantined", "repository", "scanned_at", "scanner", "severity", "status", "title", "type", "vex_status",
	}

	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}

		slices.Sort(out)

		return out
	}

	if !slices.Equal(keys(got.Findings[0]), wantKeys) {
		t.Fatalf("keys = %v, want %v", keys(got.Findings[0]), wantKeys)
	}

	f := got.Findings[0]
	if f["severity"] != "critical" || f["title"] != "CVE-2026-1" || f["vex_status"] != "affected" ||
		f["scanned_at"] != at.Format(time.RFC3339Nano) {
		t.Errorf("finding = %v", f)
	}

	counts := f["counts"].(map[string]any)
	if counts["critical"] != float64(1) || counts["high"] != float64(2) || counts["medium"] != float64(0) || counts["low"] != float64(0) {
		t.Errorf("counts = %v", counts)
	}

	// no vex_status without a decision; severity none without counts
	second := got.Findings[1]
	if _, ok := second["vex_status"]; ok || second["severity"] != "none" {
		t.Errorf("second = %v", second)
	}
}

func TestSecurityFindings_DatabaseErrorsAreNotLeaked(t *testing.T) {
	for _, target := range []string{
		"/api/v1/security/findings", "/api/v1/security/findings/summary", "/api/v1/security/findings/export", "/api/v1/security/health",
	} {
		t.Run(target, func(t *testing.T) {
			db := newSecurityMockDB()
			db.err = errors.New("connection to 10.1.2.3 refused: password=hunter2")

			rw := serveSecurity(t, db, adminPrincipal(), target)
			if rw.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rw.Code)
			}

			if strings.Contains(rw.Body.String(), "hunter2") || strings.Contains(rw.Body.String(), "10.1.2.3") {
				t.Fatalf("the database error leaked: %s", rw.Body.String())
			}
		})
	}
}

func TestSecurityFindings_InvalidFilterFromDatabaseIs400(t *testing.T) {
	db := newSecurityMockDB()
	db.err = database.ErrInvalidFindingFilter

	if rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings"); rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rw.Code)
	}
}

func TestSecuritySummary_Response(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	db := newSecurityMockDB()
	db.summary = &models.FindingsSummary{
		ByType: map[string]models.FindingTypeSummary{
			"vuln":    {Total: 9, Critical: 1, High: 2, Medium: 3, Low: 3, Quarantined: 4},
			"license": {Total: 2, High: 2, Quarantined: 2},
		},
		ScannedRepositories: 5, TotalRepositories: 7, LastScanAt: &at,
	}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/summary?repo=npm-main&project=acme")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	if f := db.summaryCalls[0]; f.Repo != "npm-main" || f.Project != "acme" {
		t.Fatalf("filter = %+v", f)
	}

	var got struct {
		ByType              map[string]map[string]int `json:"by_type"`
		ScannedRepositories int                       `json:"scanned_repositories"`
		TotalRepositories   int                       `json:"total_repositories"`
		LastScanAt          *string                   `json:"last_scan_at"`
	}

	decodeBody(t, rw, &got)

	if got.ScannedRepositories != 5 || got.TotalRepositories != 7 || got.LastScanAt == nil ||
		*got.LastScanAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("body = %s", rw.Body.String())
	}

	wantVuln := map[string]int{"total": 9, "critical": 1, "high": 2, "medium": 3, "low": 3, "quarantined": 4}
	for k, v := range wantVuln {
		if got.ByType["vuln"][k] != v {
			t.Errorf("vuln.%s = %d, want %d", k, got.ByType["vuln"][k], v)
		}
	}

	// every type is present, also without findings
	for _, typ := range models.FindingTypes() {
		if _, ok := got.ByType[typ]; !ok {
			t.Errorf("by_type.%s missing", typ)
		}
	}
}

func TestSecurityExport_CSVAndJSON(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	epss := 0.5

	db := newSecurityMockDB()
	db.pages = []*models.FindingPage{{Findings: []*models.Finding{{
		ID: secFindingID, Type: "secret", Repository: "r", Format: "npm", ArtifactID: "a@1", PackageName: "a", PackageVersion: "1",
		High: 1, Status: "open", SeverityRank: 3, Title: "=HYPERLINK(\"http://evil\")", ScannedAt: at,
		KEVCount: 1, MaxEPSS: &epss,
	}}}}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export?format=csv&type=secret")
	if rw.Code != http.StatusOK || !strings.HasPrefix(rw.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rw.Header().Get("Content-Disposition"), "security-findings.csv") {
		t.Fatalf("status %d, headers %v", rw.Code, rw.Header())
	}

	if rw.Header().Get("X-Export-Truncated") != "" {
		t.Error("a complete export must not be marked truncated")
	}

	rows, err := csv.NewReader(rw.Body).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}

	if len(rows) != 2 || !slices.Equal(rows[0], securityCSVHeader()) {
		t.Fatalf("rows = %v", rows)
	}

	// the exploit columns come last; the earlier ones keep their positions
	if h := securityCSVHeader(); !slices.Equal(h[len(h)-4:], []string{"id", "kev_count", "max_epss", "priority"}) ||
		slices.Index(h, "id") != 17 {
		t.Fatalf("header = %v", h)
	}

	if got := rows[1][18:]; !slices.Equal(got, []string{"1", "0.5", "kev"}) {
		t.Fatalf("exploit columns = %v", got)
	}

	title := rows[1][slices.Index(securityCSVHeader(), "title")]
	if strings.HasPrefix(title, "=") {
		t.Fatalf("title %q is a spreadsheet formula", title)
	}

	if got := db.listCalls[0]; got.Limit != securityFindingsMaxLimit || !slices.Equal(got.Types, []string{"secret"}) {
		t.Fatalf("query = %+v", got)
	}

	db.pages = []*models.FindingPage{{Findings: []*models.Finding{{ID: secFindingID, Type: "vuln", ScannedAt: at}}}}

	rw = serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export")

	var body struct {
		Findings  []map[string]any `json:"findings"`
		Truncated bool             `json:"truncated"`
	}

	decodeBody(t, rw, &body)

	if rw.Header().Get("Content-Type") != "application/json" || len(body.Findings) != 1 || body.Truncated {
		t.Fatalf("json export = %s (%v)", rw.Body.String(), rw.Header())
	}

	if f := body.Findings[0]; f["kev_count"] != float64(0) || f["priority"] != "none" {
		t.Fatalf("json export exploit fields = %v", f)
	}

	if rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export?format=pdf"); rw.Code != http.StatusBadRequest {
		t.Fatalf("unknown format: status = %d, want 400", rw.Code)
	}

	if rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export?format=sarif"); rw.Code != http.StatusBadRequest {
		t.Fatalf("sarif is not offered here: status = %d, want 400", rw.Code)
	}
}

func TestSecurityExport_ReadsEveryPageUpToTheCap(t *testing.T) {
	cursor := &models.FindingCursor{Sort: "severity", Rank: 1, ScannedAt: time.Now().UTC(), ID: secFindingID}
	mk := func(n int) []*models.Finding {
		out := make([]*models.Finding, n)
		for i := range out {
			out[i] = &models.Finding{ID: secFindingID, Type: "ai"}
		}

		return out
	}

	db := newSecurityMockDB()
	db.pages = []*models.FindingPage{
		{Findings: mk(3), Next: cursor},
		{Findings: mk(2)},
	}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export")

	var body struct {
		Findings []map[string]any `json:"findings"`
	}

	decodeBody(t, rw, &body)

	if len(body.Findings) != 5 || len(db.listCalls) != 2 || db.listCalls[1].After == nil {
		t.Fatalf("findings = %d, calls = %d", len(body.Findings), len(db.listCalls))
	}

	// a selection above the cap is cut and says so
	db = newSecurityMockDB()
	db.pages = []*models.FindingPage{{Findings: mk(securityFindingsMaxLimit), Next: cursor}} // served again for every page

	rw = serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export")
	if rw.Code != http.StatusOK || rw.Header().Get("X-Export-Truncated") != "true" {
		t.Fatalf("status = %d, truncated header %q", rw.Code, rw.Header().Get("X-Export-Truncated"))
	}

	var capped struct {
		Findings  []map[string]any `json:"findings"`
		Truncated bool             `json:"truncated"`
	}

	decodeBody(t, rw, &capped)

	if len(capped.Findings) != securityExportMaxFindings || !capped.Truncated {
		t.Fatalf("findings = %d (cap %d), truncated = %v", len(capped.Findings), securityExportMaxFindings, capped.Truncated)
	}
}

func TestSecurityExport_VEX(t *testing.T) {
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	db := newSecurityMockDB()
	db.vulnScans = map[string]*models.VulnScanResult{
		"lodash@4.0.0": {
			ArtifactID: "lodash@4.0.0", ScanStatus: "completed", VulnCount: 1,
			Vulnerabilities: []models.Vulnerability{{ID: "CVE-2026-1", Severity: "CRITICAL", Summary: "boom"}},
		},
	}
	// the same package hosted in two repositories, a license finding that VEX ignores
	db.pages = []*models.FindingPage{{Findings: []*models.Finding{
		{
			ID:           secFindingID,
			Type:         "vuln",
			RepositoryID: "repo-1",
			Repository:   "npm-a",
			Format:       "npm",
			ArtifactID:   "lodash@4.0.0",
			ScannedAt:    at,
		},
		{
			ID:           "9a000000-0000-4000-8000-000000000002",
			Type:         "vuln",
			RepositoryID: "repo-2",
			Repository:   "npm-b",
			Format:       "npm",
			ArtifactID:   "lodash@4.0.0",
			ScannedAt:    at,
		},
	}}}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export?format=vex&type=vuln,license")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	if ct := rw.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}

	if cd := rw.Header().Get("Content-Disposition"); !strings.Contains(cd, "security-findings-vex.cyclonedx") {
		t.Errorf("content disposition = %q", cd)
	}

	if got := db.listCalls[0]; !slices.Equal(got.Types, []string{"vuln"}) {
		t.Errorf("the VEX export must read vulnerability findings only, got %v", got.Types)
	}

	var doc map[string]any

	decodeBody(t, rw, &doc)

	vulns, _ := doc["vulnerabilities"].([]any)
	if doc["bomFormat"] != "CycloneDX" || len(vulns) != 1 || vulns[0].(map[string]any)["id"] != "CVE-2026-1" {
		t.Fatalf("document = %s", rw.Body.String())
	}

	// no vuln type requested: an empty document, no query
	db = newSecurityMockDB()

	rw = serveSecurity(t, db, adminPrincipal(), "/api/v1/security/findings/export?format=vex&type=license")
	if rw.Code != http.StatusOK || len(db.listCalls) != 0 {
		t.Fatalf("status = %d, queries = %d", rw.Code, len(db.listCalls))
	}

	var empty struct {
		Vulnerabilities []any `json:"vulnerabilities"`
	}

	decodeBody(t, rw, &empty)

	if len(empty.Vulnerabilities) != 0 {
		t.Fatalf("document = %s", rw.Body.String())
	}
}

func TestSecurityRepoSummary_Access(t *testing.T) {
	scanned := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	callers := []struct {
		name      string
		principal *auth.Principal
		want      int
	}{
		{"global admin", &auth.Principal{UserID: new(uAdmin1)}, http.StatusOK},
		{"maintainer of the repository's project", &auth.Principal{UserID: new(uMaint1)}, http.StatusOK},
		{"developer of the repository's project", &auth.Principal{UserID: new(uDev1)}, http.StatusOK},
		{"viewer of the repository's project", &auth.Principal{UserID: new(uView1)}, http.StatusOK},
		{"user without a role", &auth.Principal{UserID: new(uPlain1)}, http.StatusForbidden},
		{"anonymous on a private repository", &auth.Principal{IsAnonymous: true}, http.StatusForbidden},
		{"no principal", nil, http.StatusUnauthorized},
	}

	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			db := newSecurityMockDB()
			db.repos["app"] = &models.Repository{ID: "repo-1", Name: "app", ProjectID: new(secProjectA)}
			db.repoSum = &models.RepoSecuritySummary{ScannedAt: &scanned}

			rw := serveSecurity(t, db, c.principal, "/api/v1/repositories/app/security/summary")
			if rw.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, c.want, rw.Body.String())
			}

			if c.want != http.StatusOK && len(db.repoSumCalls) != 0 {
				t.Fatal("the database was queried for a caller without read access")
			}
		})
	}
}

func TestSecurityRepoSummary_PublicRepositoryIsReadableAnonymously(t *testing.T) {
	db := newSecurityMockDB()
	db.repos["pub"] = &models.Repository{ID: "repo-1", Name: "pub", Public: true}
	db.repoSum = &models.RepoSecuritySummary{}

	if rw := serveSecurity(
		t,
		db,
		&auth.Principal{IsAnonymous: true},
		"/api/v1/repositories/pub/security/summary",
	); rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rw.Code, rw.Body.String())
	}
}

func TestSecurityRepoSummary_Response(t *testing.T) {
	scanned := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	db := newSecurityMockDB()
	db.repos["app"] = &models.Repository{ID: "repo-1", Name: "app"}
	db.repoSum = &models.RepoSecuritySummary{
		ScannedAt: &scanned, VulnCritical: 1, VulnHigh: 2, VulnMedium: 3, VulnLow: 4, LicenseViolations: 5, SecretCount: 6,
		AIQuarantineRecommended: 7, AIReviewRequired: 8, Quarantined: 2,
	}

	rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/repositories/app/security/summary?package_name=lodash&version=4.0.0")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	if !slices.Equal(db.repoSumCalls, []string{"lodash@4.0.0"}) || db.repoSumRepoID != "repo-1" {
		t.Fatalf("query = %v repo %q", db.repoSumCalls, db.repoSumRepoID)
	}

	var got map[string]any
	decodeBody(t, rw, &got)

	want := map[string]any{
		"scanned_at":  scanned.Format(time.RFC3339Nano),
		"vuln":        map[string]any{"critical": float64(1), "high": float64(2), "medium": float64(3), "low": float64(4)},
		"license":     map[string]any{"violations": float64(5)},
		"secret":      map[string]any{"count": float64(6)},
		"ai":          map[string]any{"quarantine_recommended": float64(7), "review_required": float64(8)},
		"quarantined": true,
	}

	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)

	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("body = %s, want %s", gotJSON, wantJSON)
	}
}

// TestSecurityRepoSummary_NeverLeaksFindingDetails: the counts-only endpoint
// carries numbers, a time and a flag, whatever the database holds; the key set
// is exact so a new field cannot slip details in unnoticed.
func TestSecurityRepoSummary_NeverLeaksFindingDetails(t *testing.T) {
	db := newSecurityMockDB()
	db.repos["app"] = &models.Repository{ID: "repo-1", Name: "app", ProjectID: new(secProjectA)}
	db.repoSum = &models.RepoSecuritySummary{
		VulnCritical:            1,
		LicenseViolations:       1,
		SecretCount:             1,
		AIQuarantineRecommended: 1,
		Quarantined:             1,
	}
	// findings that exist in the store must not surface through the summary
	db.pages = []*models.FindingPage{{Findings: []*models.Finding{{
		ID: secFindingID, Title: "CVE-2026-SECRET-TITLE", ArtifactID: "internal-lib@9.9.9", PackageName: "internal-lib",
	}}}}

	rw := serveSecurity(t, db, &auth.Principal{UserID: new(uView1), Username: "view-1"}, "/api/v1/repositories/app/security/summary")
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rw.Code, rw.Body.String())
	}

	body := rw.Body.String()
	for _, leak := range []string{"CVE-2026-SECRET-TITLE", "internal-lib", secFindingID, "title", "artifact_id", "package_name", "\"id\"", "\"findings\""} {
		if strings.Contains(body, leak) {
			t.Fatalf("the counts-only summary leaks %q: %s", leak, body)
		}
	}

	var got map[string]any
	decodeBody(t, rw, &got)

	assertKeys := func(what string, m map[string]any, want ...string) {
		t.Helper()

		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}

		slices.Sort(keys)
		slices.Sort(want)

		if !slices.Equal(keys, want) {
			t.Errorf("%s keys = %v, want %v", what, keys, want)
		}
	}

	assertKeys("summary", got, "scanned_at", "vuln", "license", "secret", "ai", "quarantined")
	assertKeys("vuln", got["vuln"].(map[string]any), "critical", "high", "medium", "low")
	assertKeys("license", got["license"].(map[string]any), "violations")
	assertKeys("secret", got["secret"].(map[string]any), "count")
	assertKeys("ai", got["ai"].(map[string]any), "quarantine_recommended", "review_required")

	if _, isBool := got["quarantined"].(bool); !isBool {
		t.Errorf("quarantined = %v, want a bool", got["quarantined"])
	}

	if got["scanned_at"] != nil {
		t.Errorf("scanned_at = %v, want null for a never scanned selection", got["scanned_at"])
	}
}

func TestSecurityRepoSummary_Validation(t *testing.T) {
	long := strings.Repeat("x", maxNameLen+1)

	for name, tc := range map[string]struct {
		query string
		want  int
	}{
		"version needs a package":  {"version=1.0.0", http.StatusBadRequest},
		"package too long":         {"package_name=" + long, http.StatusBadRequest},
		"version too long":         {"package_name=a&version=" + long, http.StatusBadRequest},
		"package alone is allowed": {"package_name=a", http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			db := newSecurityMockDB()
			db.repos["app"] = &models.Repository{ID: "repo-1", Name: "app"}
			db.repoSum = &models.RepoSecuritySummary{}

			rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/repositories/app/security/summary?"+tc.query)
			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rw.Code, tc.want, rw.Body.String())
			}
		})
	}

	db := newSecurityMockDB()
	if rw := serveSecurity(t, db, adminPrincipal(), "/api/v1/repositories/ghost/security/summary"); rw.Code != http.StatusNotFound {
		t.Fatalf("unknown repository: status = %d, want 404", rw.Code)
	}
}
