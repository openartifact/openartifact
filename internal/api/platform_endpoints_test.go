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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	platRepoID    = "aaaaaaaa-0000-4000-8000-000000000001"
	platProjectID = "aaaaaaaa-0000-4000-8000-000000000002"
	platUserID    = "aaaaaaaa-0000-4000-8000-000000000003"
	platCursorID  = "aaaaaaaa-0000-4000-8000-000000000005"
)

// platformMockDBStore is what platformMockDB is used as.
type platformMockDBStore interface {
	AdminAuditStore
	AdminTokensStore
	QuotasStore
	RepositoryActivityStore
	auth.EvaluatorStore
}

// platformMockDB backs the token, quota, usage and activity endpoints.
type platformMockDB struct {
	platformMockDBStore

	repo      *models.Repository
	repoErr   error
	quota     *models.StorageQuota
	usage     *models.RepositoryUsage
	audit     *models.AuditPage
	names     *models.AuditNames
	namesErr  error
	grants    []*models.ProjectGrant
	tokens    []*models.TokenListItem
	overview  []*models.QuotaOverview
	listErr   error
	auditSeen models.AuditListParams
	tokSeen   models.TokenListParams
	quotaSeen models.QuotaListParams

	resolveCalls int
	resolveRefs  models.AuditNameRefs
}

func (db *platformMockDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return nil, nil
}

func (db *platformMockDB) GetRepositoryByName(context.Context, string) (*models.Repository, error) {
	return db.repo, db.repoErr
}

func (db *platformMockDB) GetOrCreateStorageQuota(context.Context, string) (*models.StorageQuota, error) {
	return db.quota, nil
}

func (db *platformMockDB) GetRepositoryUsage(context.Context, string) (*models.RepositoryUsage, error) {
	return db.usage, nil
}

func (db *platformMockDB) ListAuditLogs(_ context.Context, p models.AuditListParams) (*models.AuditPage, error) {
	db.auditSeen = p
	return db.audit, db.listErr
}

func (db *platformMockDB) ResolveAuditNames(_ context.Context, refs models.AuditNameRefs) (*models.AuditNames, error) {
	db.resolveCalls++
	db.resolveRefs = refs

	return db.names, db.namesErr
}

func (db *platformMockDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return db.grants, nil
}

func (db *platformMockDB) ListUnifiedTokens(_ context.Context, p models.TokenListParams) ([]*models.TokenListItem, int, error) {
	db.tokSeen = p
	return db.tokens, len(db.tokens), db.listErr
}

func (db *platformMockDB) ListQuotaOverview(_ context.Context, p models.QuotaListParams) ([]*models.QuotaOverview, int, error) {
	db.quotaSeen = p
	return db.overview, len(db.overview), db.listErr
}

func platformRouter(t *testing.T, db *platformMockDB) chi.Router {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	ev := auth.NewEvaluator(db, log, false)
	r := chi.NewRouter()
	NewAdminTokensHandler(db, log, ev).Register(r)
	NewQuotasHandler(log, db, ev).RegisterRoutes(r, "/api/v1/repositories/{name}")
	NewQuotasHandler(log, db, ev).RegisterAdminRoutes(r)
	NewRepositoryActivityHandler(db, log, ev).RegisterAuthed(r)
	NewAdminAuditHandler(db, log, ev).Register(r)

	return r
}

func platformGet(t *testing.T, r http.Handler, p *auth.Principal, target string) (int, map[string]any) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	var body map[string]any

	_ = json.Unmarshal(rw.Body.Bytes(), &body)

	return rw.Code, body
}

func platAdminP() *auth.Principal { return &auth.Principal{Username: "admin", IsLegacy: true} }
func platAnonP() *auth.Principal  { return &auth.Principal{IsAnonymous: true} }

func platUser() *auth.Principal {
	id := platUserID
	return &auth.Principal{UserID: &id, Username: "dana", Credential: auth.CredentialSession}
}

func platAPIToken() *auth.Principal {
	p := platUser()
	p.Credential, p.TokenKind = auth.CredentialBearerToken, "personal"

	return p
}

func TestUnifiedTokensAdminSeesEverythingAndFlagsExpired(t *testing.T) {
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	db := &platformMockDB{tokens: []*models.TokenListItem{
		{ID: "t1", Type: models.TokenListPersonal, Name: "ci", Owner: "dana", CreatedAt: past, ExpiresAt: &past},
		{
			ID:        "t2",
			Type:      models.TokenListProject,
			Name:      "deploy",
			Owner:     "lee",
			Project:   "acme",
			Role:      "developer",
			CreatedAt: past,
			ExpiresAt: &future,
		},
		{ID: "t3", Type: models.TokenListFederation, Name: "peer", Owner: "lee", Repository: "r", CreatedAt: past},
	}}

	code, body := platformGet(
		t,
		platformRouter(t, db),
		platAdminP(),
		"/api/v1/tokens?type=project&project=acme&q=dep&expires_before=2030-01-02&page=2&limit=500",
	)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if !db.tokSeen.Admin || db.tokSeen.ViewerID != "" || db.tokSeen.ProjectIDs != nil {
		t.Errorf("admin query must be unrestricted: %+v", db.tokSeen)
	}

	if !slices.Equal(db.tokSeen.Types, []string{"project"}) || db.tokSeen.Query != "dep" || db.tokSeen.Project != "acme" ||
		db.tokSeen.Page != 2 ||
		db.tokSeen.Limit != unifiedTokensMaxLimit {
		t.Errorf("query = %+v", db.tokSeen)
	}

	if db.tokSeen.ExpiresBefore == nil || db.tokSeen.ExpiresBefore.Format(time.DateOnly) != "2030-01-02" {
		t.Errorf("expires_before = %v", db.tokSeen.ExpiresBefore)
	}

	toks := body["tokens"].([]any)
	if len(toks) != 3 || body["total"] != float64(3) || body["page"] != float64(2) {
		t.Fatalf("body = %v", body)
	}

	for i, want := range []bool{true, false, false} {
		if got := toks[i].(map[string]any)["expired"]; got != want {
			t.Errorf("token %d expired = %v, want %v", i, got, want)
		}
	}

	for _, e := range toks {
		if _, leaked := e.(map[string]any)["token_hash"]; leaked {
			t.Errorf("secret material in %v", e)
		}
	}
}

func TestUnifiedTokensNonAdminIsScopedToOwnAndMaintainedProjects(t *testing.T) {
	db := &platformMockDB{grants: []*models.ProjectGrant{
		{ProjectID: "p-owner", Role: models.ProjectRoleOwner},
		{ProjectID: "p-maint", Role: models.ProjectRoleMaintainer},
		{ProjectID: "p-dev", Role: models.ProjectRoleDeveloper},
		{ProjectID: "p-view", Role: models.ProjectRoleViewer},
	}}

	code, _ := platformGet(t, platformRouter(t, db), platUser(), "/api/v1/tokens")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	got := slices.Clone(db.tokSeen.ProjectIDs)
	slices.Sort(got)

	if db.tokSeen.Admin || db.tokSeen.ViewerID != platUserID || !slices.Equal(got, []string{"p-maint", "p-owner"}) {
		t.Errorf("scope = %+v", db.tokSeen)
	}

	if db.tokSeen.Page != 1 || db.tokSeen.Limit != unifiedTokensDefaultLimit {
		t.Errorf("defaults = page %d limit %d", db.tokSeen.Page, db.tokSeen.Limit)
	}
}

func TestUnifiedTokensRejections(t *testing.T) {
	tests := []struct {
		name   string
		p      *auth.Principal
		target string
		want   int
	}{
		{"anonymous", platAnonP(), "/api/v1/tokens", http.StatusUnauthorized},
		{"non-admin API token", platAPIToken(), "/api/v1/tokens", http.StatusForbidden},
		{"bad type", platAdminP(), "/api/v1/tokens?type=other", http.StatusBadRequest},
		{"bad expires_before", platAdminP(), "/api/v1/tokens?expires_before=soon", http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &platformMockDB{}
			if code, _ := platformGet(t, platformRouter(t, db), tc.p, tc.target); code != tc.want {
				t.Errorf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestUnifiedTokensDatabaseErrorIsOpaque(t *testing.T) {
	db := &platformMockDB{listErr: errors.New("secret dsn leak")}
	r := platformRouter(t, db)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/tokens", nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), platAdminP()))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	if rw.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rw.Code)
	}

	if strings.Contains(rw.Body.String(), "secret dsn leak") {
		t.Errorf("body leaks the error: %s", rw.Body.String())
	}
}

func TestRepositoryUsage(t *testing.T) {
	published := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	db := &platformMockDB{
		repo:  &models.Repository{ID: platRepoID, Name: "r"},
		quota: &models.StorageQuota{MaxSizeBytes: 1000, WarnThresholdPercent: 80, CurrentSizeBytes: 250, CurrentArtifactCount: 3},
		usage: &models.RepositoryUsage{PackageCount: 2, VersionCount: 5, LastPublishedAt: &published},
	}

	code, body := platformGet(t, platformRouter(t, db), platAdminP(), "/api/v1/repositories/r/usage")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	q := body["quota"].(map[string]any)

	want := map[string]any{"package_count": 2.0, "version_count": 5.0, "size_bytes": 250.0, "last_published_at": "2026-03-04T05:06:07Z"}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}

	wantQ := map[string]any{"limit_bytes": 1000.0, "used_bytes": 250.0, "soft_limit_bytes": 800.0, "usage_percent": 25.0, "exceeded": false}
	for k, v := range wantQ {
		if q[k] != v {
			t.Errorf("quota.%s = %v, want %v", k, q[k], v)
		}
	}
}

func TestRepositoryUsageUnlimitedOmitsLimits(t *testing.T) {
	db := &platformMockDB{
		repo:  &models.Repository{ID: platRepoID, Name: "r"},
		quota: &models.StorageQuota{WarnThresholdPercent: 80, CurrentSizeBytes: 9},
		usage: &models.RepositoryUsage{},
	}

	_, body := platformGet(t, platformRouter(t, db), platAdminP(), "/api/v1/repositories/r/usage")
	q := body["quota"].(map[string]any)

	for _, k := range []string{"limit_bytes", "soft_limit_bytes", "max_artifact_count"} {
		if _, ok := q[k]; ok {
			t.Errorf("unlimited quota carries %s: %v", k, q)
		}
	}

	if _, ok := body["last_published_at"]; ok {
		t.Errorf("empty repository carries last_published_at: %v", body)
	}
}

func TestRepositoryUsageAndActivityAuthorization(t *testing.T) {
	for _, path := range []string{"/api/v1/repositories/r/usage", "/api/v1/repositories/r/activity"} {
		t.Run(path, func(t *testing.T) {
			db := &platformMockDB{
				repo:  &models.Repository{ID: platRepoID, Name: "r"},
				quota: &models.StorageQuota{},
				usage: &models.RepositoryUsage{},
				audit: &models.AuditPage{},
			}

			for _, p := range []*auth.Principal{platAnonP(), platUser()} {
				if code, _ := platformGet(t, platformRouter(t, db), p, path); code != http.StatusForbidden {
					t.Errorf("%+v: status = %d, want 403", p, code)
				}
			}

			db.repo = nil
			if code, _ := platformGet(t, platformRouter(t, db), platAdminP(), path); code != http.StatusNotFound {
				t.Errorf("unknown repository: status = %d, want 404", code)
			}
		})
	}
}

func TestAdminQuotasListAndFilters(t *testing.T) {
	db := &platformMockDB{overview: []*models.QuotaOverview{
		{
			Scope:                models.QuotaScopeRepository,
			Name:                 "r",
			Project:              "acme",
			MaxSizeBytes:         100,
			WarnThresholdPercent: 90,
			UsedBytes:            100,
			ArtifactCount:        4,
		},
		{Scope: models.QuotaScopeProject, Name: "acme", UsedBytes: 7, WarnThresholdPercent: 80},
	}}

	code, body := platformGet(t, platformRouter(t, db), platAdminP(), "/api/v1/admin/quotas?scope=repository&limited=true&q=r")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if db.quotaSeen.Scope != "repository" || !db.quotaSeen.LimitedOnly || db.quotaSeen.Query != "r" ||
		db.quotaSeen.Limit != quotaOverviewDefaultLimit {
		t.Errorf("query = %+v", db.quotaSeen)
	}

	quotas := body["quotas"].([]any)
	first, second := quotas[0].(map[string]any), quotas[1].(map[string]any)

	if first["scope"] != "repository" || first["name"] != "r" || first["project"] != "acme" || first["limit_bytes"] != 100.0 ||
		first["exceeded"] != true ||
		first["usage_percent"] != 100.0 {
		t.Errorf("first = %v", first)
	}

	if _, ok := second["limit_bytes"]; ok || second["scope"] != "project" {
		t.Errorf("unlimited project line = %v", second)
	}
}

func TestAdminQuotasRejections(t *testing.T) {
	db := &platformMockDB{}

	if code, _ := platformGet(t, platformRouter(t, db), platUser(), "/api/v1/admin/quotas"); code != http.StatusForbidden {
		t.Errorf("non-admin status = %d, want 403", code)
	}

	if code, _ := platformGet(t, platformRouter(t, db), platAdminP(), "/api/v1/admin/quotas?scope=galaxy"); code != http.StatusBadRequest {
		t.Errorf("bad scope status = %d, want 400", code)
	}
}

func platformAuditRows() []*models.AuditLog {
	repo, proj, actor, rt := platRepoID, platProjectID, platUserID, "repository"
	pkgType, pkgID := "npm_version", "row-1"
	userType, userRT, userID := "user", "user", platUserID

	return []*models.AuditLog{
		{
			ID:           "e1",
			Action:       "repository.update",
			RepositoryID: &repo,
			ProjectID:    &proj,
			ActorID:      &actor,
			ActorType:    &userType,
			ResourceType: &rt,
			ResourceID:   &repo,
			CreatedAt:    time.Now(),
		},
		{
			ID:           "e2",
			Action:       "trash.delete",
			RepositoryID: &repo,
			ResourceType: &pkgType,
			ResourceID:   &pkgID,
			DetailsJSON:  json.RawMessage(`{"actor":"cleanup-policy","package":"left-pad","version":"1.0.0"}`),
			CreatedAt:    time.Now(),
		},
		{
			ID:           "e3",
			Action:       "user.update",
			ActorID:      &actor,
			ActorType:    &userType,
			ResourceType: &userRT,
			ResourceID:   &userID,
			CreatedAt:    time.Now(),
		},
		{ID: "e4", Action: "http.request", DetailsJSON: json.RawMessage(`{"method":"GET"}`), CreatedAt: time.Now()},
	}
}

func platformNames() *models.AuditNames {
	n := models.NewAuditNames()
	n.Users[platUserID] = "dana"
	n.Repositories[platRepoID] = "payments"
	n.Projects[platProjectID] = "acme"

	return n
}

func TestRepositoryActivityResolvesNamesInOneBatch(t *testing.T) {
	db := &platformMockDB{
		repo:  &models.Repository{ID: platRepoID, Name: "payments"},
		audit: &models.AuditPage{Logs: platformAuditRows()},
		names: platformNames(),
	}

	code, body := platformGet(
		t,
		platformRouter(t, db),
		platAdminP(),
		"/api/v1/repositories/payments/activity?package_name=left-pad&version=1.0.0&limit=10",
	)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if db.resolveCalls != 1 {
		t.Errorf("ResolveAuditNames calls = %d, want 1 for the whole page", db.resolveCalls)
	}

	p := db.auditSeen
	if p.RepositoryID != platRepoID || p.PackageName != "left-pad" || p.PackageVersion != "1.0.0" || p.Limit != 10 || !p.NoTotal {
		t.Errorf("query = %+v", p)
	}

	checkResolvedActivityRows(t, body["data"].([]any))
}

func checkResolvedActivityRows(t *testing.T, data []any) {
	t.Helper()

	e := func(i int) map[string]any { return data[i].(map[string]any) }

	if e(0)["actor_name"] != "dana" {
		t.Errorf("actor resolved from id = %v", e(0)["actor_name"])
	}

	if s := e(0)["subject"].(map[string]any); s["kind"] != "repository" || s["name"] != "payments" || s["project"] != "acme" ||
		s["repository"] != "payments" {
		t.Errorf("repository subject = %v", s)
	}

	if e(1)["actor_name"] != "cleanup-policy" {
		t.Errorf("recorded actor = %v", e(1)["actor_name"])
	}

	if s := e(1)["subject"].(map[string]any); s["kind"] != "package" || s["name"] != "left-pad" || s["version"] != "1.0.0" {
		t.Errorf("package subject = %v", s)
	}

	if s := e(2)["subject"].(map[string]any); s["kind"] != "user" || s["name"] != "dana" {
		t.Errorf("user subject = %v", s)
	}

	if e(3)["subject"] != nil || e(3)["actor_name"] != "" {
		t.Errorf("generic request row names nothing: %v", e(3))
	}

	for _, entry := range data {
		if _, leaked := entry.(map[string]any)["ip"]; leaked {
			t.Errorf("client address leaked: %v", entry)
		}
	}
}

func TestRepositoryActivityKeysetPaging(t *testing.T) {
	cur := &models.AuditCursor{CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), ID: platCursorID}
	db := &platformMockDB{
		repo:  &models.Repository{ID: platRepoID, Name: "payments"},
		audit: &models.AuditPage{Logs: platformAuditRows(), Next: cur},
		names: platformNames(),
	}

	_, body := platformGet(t, platformRouter(t, db), platAdminP(), "/api/v1/repositories/payments/activity?cursor="+encodeAuditCursor(cur))

	if db.auditSeen.After == nil || db.auditSeen.After.ID != platCursorID || db.auditSeen.Limit != activityDefaultPerPage {
		t.Errorf("query = %+v", db.auditSeen)
	}

	if body["has_more"] != true || body["next_cursor"] == "" {
		t.Errorf("paging = %v", body)
	}

	if _, ok := body["total"]; ok {
		t.Errorf("activity must not count rows: %v", body)
	}

	if code, _ := platformGet(
		t,
		platformRouter(t, db),
		platAdminP(),
		"/api/v1/repositories/payments/activity?cursor=%21%21",
	); code != http.StatusBadRequest {
		t.Errorf("malformed cursor status = %d, want 400", code)
	}
}

func TestResolveAuditEntriesDegradesWhenLookupFails(t *testing.T) {
	db := &platformMockDB{
		repo:     &models.Repository{ID: platRepoID, Name: "payments"},
		audit:    &models.AuditPage{Logs: platformAuditRows()},
		namesErr: errors.New("lookup down"),
	}

	code, body := platformGet(t, platformRouter(t, db), platAdminP(), "/api/v1/repositories/payments/activity")
	if code != http.StatusOK {
		t.Fatalf("a failed name lookup must not fail the feed: status = %d", code)
	}

	data := body["data"].([]any)
	if got := data[1].(map[string]any)["actor_name"]; got != "cleanup-policy" {
		t.Errorf("recorded actor must survive: %v", got)
	}

	if s := data[0].(map[string]any)["subject"].(map[string]any); s["name"] != nil {
		t.Errorf("unresolved repository name must be null, got %v", s["name"])
	}
}

func TestAuditSubjectOfDeletedSubjectUsesRecordedName(t *testing.T) {
	rt, rid := "repository", platRepoID
	l := &models.AuditLog{ResourceType: &rt, ResourceID: &rid}

	s := auditSubjectOf(l, auditDetails{Subject: "gone-repo"}, models.NewAuditNames())
	if s == nil || s.Name == nil || *s.Name != "gone-repo" {
		t.Errorf("subject = %+v", s)
	}

	if s := auditSubjectOf(l, auditDetails{}, models.NewAuditNames()); s == nil || s.Name != nil {
		t.Errorf("without a recorded name the name is null: %+v", s)
	}
}

func TestQuotaLineExceededByCount(t *testing.T) {
	line := quotaLine(0, 5, 80, 10, 5)
	if !line.Exceeded || line.LimitBytes != nil || line.MaxArtifactCount == nil || *line.MaxArtifactCount != 5 {
		t.Errorf("line = %+v", line)
	}

	if line := quotaLine(1000, 0, 80, 999, 1); line.Exceeded || line.UsagePercent != 99 {
		t.Errorf("line = %+v", line)
	}
}

func TestAdminAuditListCarriesActorNameAndSubject(t *testing.T) {
	db := &platformMockDB{audit: &models.AuditPage{Logs: platformAuditRows()}, names: platformNames()}

	code, body := platformGet(t, platformRouter(t, db), platAdminP(), "/api/v1/admin/audit?cursor=")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if db.resolveCalls != 1 {
		t.Errorf("ResolveAuditNames calls = %d, want 1 for the whole page", db.resolveCalls)
	}

	data := body["data"].([]any)
	if len(data) != 4 {
		t.Fatalf("entries = %d", len(data))
	}

	first := data[0].(map[string]any)
	if first["actor_name"] != "dana" {
		t.Errorf("actor_name = %v", first["actor_name"])
	}

	if s, ok := first["subject"].(map[string]any); !ok || s["kind"] != "repository" || s["name"] != "payments" {
		t.Errorf("subject = %v", first["subject"])
	}

	if last := data[3].(map[string]any); last["subject"] != nil {
		t.Errorf("a generic request row has no subject: %v", last)
	}
}

func TestProjectsListCarriesStatsFromOneBatchedLookup(t *testing.T) {
	db := newProjectsMock()
	db.userProjects[uDev1] = []*models.Project{db.projects["acme"]}
	db.stats = map[string]*models.ProjectStats{testAcmeID: {MemberCount: 4, SizeBytes: 2048, MaxSizeBytes: 10_000}}

	code, body := projectJSON(t, newProjectsRouter(t, db), uDev1, http.MethodGet, "/api/v1/projects", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if db.statsCalls != 1 {
		t.Errorf("ListProjectStats calls = %d, want 1", db.statsCalls)
	}

	p := body["projects"].([]any)[0].(map[string]any)
	if p["member_count"] != 4.0 || p["usage"].(map[string]any)["size_bytes"] != 2048.0 ||
		p["quota"].(map[string]any)["limit_bytes"] != 10000.0 {
		t.Errorf("project = %v", p)
	}
}

func TestProjectsListWithoutQuotaOmitsIt(t *testing.T) {
	db := newProjectsMock()
	db.userProjects[uDev1] = []*models.Project{db.projects["acme"]}
	db.stats = map[string]*models.ProjectStats{testAcmeID: {MemberCount: 1}}

	_, body := projectJSON(t, newProjectsRouter(t, db), uDev1, http.MethodGet, "/api/v1/projects", "")

	p := body["projects"].([]any)[0].(map[string]any)
	if _, ok := p["quota"]; ok {
		t.Errorf("project without a size limit carries a quota: %v", p)
	}

	if p["usage"].(map[string]any)["size_bytes"] != 0.0 {
		t.Errorf("usage = %v", p["usage"])
	}
}

func TestProjectsListNameFilter(t *testing.T) {
	for q, want := range map[string]int{"": 1, "AC": 1, "cm": 1, "zzz": 0} {
		db := newProjectsMock()
		db.userProjects[uDev1] = []*models.Project{db.projects["acme"]}

		_, body := projectJSON(t, newProjectsRouter(t, db), uDev1, http.MethodGet, "/api/v1/projects?q="+q, "")
		if got := len(body["projects"].([]any)); got != want {
			t.Errorf("q=%q: %d projects, want %d", q, got, want)
		}
	}
}
