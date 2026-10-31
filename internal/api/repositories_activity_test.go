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

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	raRepoID    = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	raProjectID = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
)

// repoActivityDB is a store with one repository (in a project) and a canned
// audit page; it records the query the handler built.
type repoActivityDB struct {
	repo   *models.Repository
	grants map[string][]*models.ProjectGrant
	page   *models.AuditPage
	params models.AuditListParams
	calls  int
}

func (d *repoActivityDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if d.repo != nil && d.repo.Name == name {
		return d.repo, nil
	}

	return nil, nil
}

func (d *repoActivityDB) ListAuditLogs(_ context.Context, p models.AuditListParams) (*models.AuditPage, error) {
	d.calls++
	d.params = p

	return d.page, nil
}

func (*repoActivityDB) ResolveAuditNames(context.Context, models.AuditNameRefs) (*models.AuditNames, error) {
	return &models.AuditNames{}, nil
}

func (*repoActivityDB) GetPermissionTargetByName(context.Context, string) (*models.PermissionTarget, error) {
	return nil, nil
}

func (*repoActivityDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return nil, nil
}

func (*repoActivityDB) GetProjectByName(context.Context, string) (*models.Project, error) {
	return nil, nil
}

func (d *repoActivityDB) ListProjectGrantsByUser(_ context.Context, userID string) ([]*models.ProjectGrant, error) {
	return d.grants[userID], nil
}

func (*repoActivityDB) ListPublicRepositoryNames(context.Context) ([]string, error) { return nil, nil }

func newRepoActivityDB() *repoActivityDB {
	project := raProjectID
	actor, actorType := "alice", "user"
	repoID := raRepoID

	return &repoActivityDB{
		repo:   &models.Repository{ID: raRepoID, Name: "libs", ProjectID: &project},
		grants: map[string][]*models.ProjectGrant{},
		page: &models.AuditPage{Logs: []*models.AuditLog{{
			ID:           "11111111-1111-4111-8111-111111111111",
			Action:       audit.ActionArtifactUploaded,
			ActorType:    &actorType,
			RepositoryID: &repoID,
			DetailsJSON:  json.RawMessage(`{"message":"uploaded: lib 1.0","actor":"` + actor + `","package":"lib","version":"1.0"}`),
			CreatedAt:    activityCreated(),
		}}},
	}
}

func serveRepoActivity(t *testing.T, db *repoActivityDB, p *auth.Principal, target string) (int, map[string]any) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	r := chi.NewRouter()
	NewRepositoryActivityHandler(db, log, auth.NewEvaluator(db, log, false)).RegisterAuthed(r)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var body map[string]any

	_ = json.Unmarshal(rec.Body.Bytes(), &body)

	return rec.Code, body
}

func raAdmin() *auth.Principal { return azAdmin() }

func raReader() *auth.Principal {
	id := "cccccccc-3333-4333-8333-cccccccccccc"
	return &auth.Principal{UserID: &id, Username: "reader", Credential: auth.CredentialSession}
}

//nolint:gochecknoglobals // immutable expectation
var raReaderActions = []string{
	audit.ActionArtifactDeleted, audit.ActionArtifactPromoted, audit.ActionArtifactPurged, audit.ActionArtifactQuarantined,
	audit.ActionArtifactReleased, audit.ActionArtifactRestored, audit.ActionArtifactUploaded, audit.ActionBundlePromoted,
	audit.ActionRepositoryRestored,
}

func TestRepoActivityReaderSeesOnlyArtifactEvents(t *testing.T) {
	db := newRepoActivityDB()

	code, body := serveRepoActivity(t, db, raReader(), "/api/v1/repositories/libs/activity")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	got := slices.Clone(db.params.Actions)
	slices.Sort(got)

	if db.params.RepositoryID != raRepoID || !slices.Equal(got, raReaderActions) || !db.params.NoTotal {
		t.Errorf("query = %+v, want repository %s, actions %v", db.params, raRepoID, raReaderActions)
	}

	entries := body["data"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["actor_name"] != "alice" {
		t.Errorf("entries = %v", entries)
	}
}

func TestRepoActivityAdminSeesEveryEvent(t *testing.T) {
	db := newRepoActivityDB()

	if code, body := serveRepoActivity(t, db, raAdmin(), "/api/v1/repositories/libs/activity"); code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	if len(db.params.Actions) != 0 {
		t.Errorf("an admin's unfiltered feed must not restrict actions: %v", db.params.Actions)
	}
}

func TestRepoActivityProjectMaintainerIsAdmin(t *testing.T) {
	db := newRepoActivityDB()
	p := raReader()
	db.grants[*p.UserID] = []*models.ProjectGrant{{ProjectID: raProjectID, Role: models.ProjectRoleMaintainer}}

	if code, _ := serveRepoActivity(t, db, p, "/api/v1/repositories/libs/activity?category=settings"); code != http.StatusOK {
		t.Fatalf("maintainer asking for settings: status = %d", code)
	}

	db.grants[*p.UserID] = []*models.ProjectGrant{{ProjectID: raProjectID, Role: models.ProjectRoleViewer}}

	if code, _ := serveRepoActivity(t, db, p, "/api/v1/repositories/libs/activity?category=settings"); code != http.StatusForbidden {
		t.Fatalf("viewer asking for settings: status = %d, want 403", code)
	}
}

func TestRepoActivityAdminOnlyCategoriesAreRefusedToReaders(t *testing.T) {
	for _, cat := range []string{"settings", "cleanup", "federation", "uploads,settings"} {
		db := newRepoActivityDB()

		code, _ := serveRepoActivity(t, db, raReader(), "/api/v1/repositories/libs/activity?category="+cat)
		if code != http.StatusForbidden || db.calls != 0 {
			t.Errorf("category %q: status = %d, queries = %d, want 403 and no query", cat, code, db.calls)
		}
	}
}

func TestRepoActivityCategoryFilter(t *testing.T) {
	db := newRepoActivityDB()

	code, _ := serveRepoActivity(t, db, raReader(), "/api/v1/repositories/libs/activity?category=trash,uploads&category=trash")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	want := []string{audit.ActionArtifactPurged, audit.ActionArtifactRestored, audit.ActionArtifactUploaded, audit.ActionRepositoryRestored}
	if !slices.Equal(db.params.Actions, want) {
		t.Errorf("actions = %v, want %v", db.params.Actions, want)
	}

	db = newRepoActivityDB()
	if code, _ := serveRepoActivity(t, db, raAdmin(), "/api/v1/repositories/libs/activity?category=cleanup"); code != http.StatusOK ||
		!slices.Contains(db.params.Actions, audit.ActionCleanupRunCompleted) {
		t.Errorf("admin cleanup: status = %d, actions = %v", code, db.params.Actions)
	}

	if code, _ := serveRepoActivity(
		t,
		db,
		raAdmin(),
		"/api/v1/repositories/libs/activity?category=nonsense",
	); code != http.StatusBadRequest {
		t.Errorf("unknown category: status = %d, want 400", code)
	}
}

func TestRepoActivityEveryCategoryNamesDeclaredActions(t *testing.T) {
	for name, c := range activityCategories {
		if len(c.actions) == 0 {
			t.Errorf("category %q has no actions", name)
		}

		for _, a := range c.actions {
			if !slices.Contains(audit.KnownActions, a) {
				t.Errorf("category %q: %q is not a known audit action", name, a)
			}
		}
	}
}

func TestRepoActivityPackageCursorAndLimitReachTheQuery(t *testing.T) {
	db := newRepoActivityDB()
	db.page.Next = &models.AuditCursor{CreatedAt: activityCreated(), ID: activityLastID}

	code, body := serveRepoActivity(t, db, raAdmin(), "/api/v1/repositories/libs/activity?package_name=lib&version=1.0&limit=5")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	if db.params.PackageName != "lib" || db.params.PackageVersion != "1.0" || db.params.Limit != 5 || db.params.After != nil {
		t.Errorf("query = %+v", db.params)
	}

	next, _ := body["next_cursor"].(string)
	if body["has_more"] != true || next == "" || body["per_page"] != float64(5) {
		t.Fatalf("paging = %v", body)
	}

	if _, ok := body["total"]; ok {
		t.Errorf("activity must not carry a total: %v", body)
	}

	db.page.Next = nil

	if code, body = serveRepoActivity(t, db, raAdmin(), "/api/v1/repositories/libs/activity?cursor="+next); code != http.StatusOK {
		t.Fatalf("page 2 status = %d", code)
	}

	if db.params.After == nil || db.params.After.ID != activityLastID || body["has_more"] != false || body["next_cursor"] != nil {
		t.Errorf("page 2: after = %+v, body = %v", db.params.After, body)
	}
}

func TestRepoActivityRejectsMalformedCursor(t *testing.T) {
	db := newRepoActivityDB()

	if code, _ := serveRepoActivity(
		t,
		db,
		raAdmin(),
		"/api/v1/repositories/libs/activity?cursor=%21%21",
	); code != http.StatusBadRequest ||
		db.calls != 0 {
		t.Errorf("status = %d, queries = %d, want 400 and no query", code, db.calls)
	}
}

func TestRepoActivityUnknownRepository(t *testing.T) {
	db := newRepoActivityDB()

	if code, _ := serveRepoActivity(t, db, raAdmin(), "/api/v1/repositories/nope/activity"); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestRepoActivityAnonymousSeesNoActors(t *testing.T) {
	db := newRepoActivityDB()

	code, body := serveRepoActivity(t, db, azAnon(), "/api/v1/repositories/libs/activity")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}

	e := body["data"].([]any)[0].(map[string]any)
	if e["actor"] != "" || e["actor_name"] != "" {
		t.Errorf("anonymous caller got actor %v / %v", e["actor"], e["actor_name"])
	}

	if raw, _ := json.Marshal(body); strings.Contains(string(raw), "alice") {
		t.Errorf("response leaks the actor: %s", raw)
	}

	if code, _ := serveRepoActivity(t, db, azAnon(), "/api/v1/repositories/libs/activity?category=settings"); code != http.StatusForbidden {
		t.Errorf("anonymous settings: status = %d, want 403", code)
	}

	if code, _ := serveRepoActivity(t, db, azAnon(), "/api/v1/repositories/libs/activity?actor=alice"); code != http.StatusForbidden {
		t.Errorf("anonymous actor filter: status = %d, want 403", code)
	}
}

func TestRepoActivityNeverReturnsClientAddress(t *testing.T) {
	db := newRepoActivityDB()
	ip := "203.0.113.9"
	db.page.Logs[0].IPAddress = &ip

	_, body := serveRepoActivity(t, db, raAdmin(), "/api/v1/repositories/libs/activity")

	if raw, _ := json.Marshal(body); strings.Contains(string(raw), ip) {
		t.Errorf("response leaks the client address: %s", raw)
	}
}
