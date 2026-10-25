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
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

const (
	trashRepoID  = "11111111-1111-4111-8111-111111111111"
	otherRepoID  = "22222222-2222-4222-8222-222222222222"
	trashItemID  = "33333333-3333-4333-8333-333333333333"
	trashPkgName = "com.acme:lib"
)

// fakeTrashDB is a database.Database + database.TrashStore double. It records
// what the handlers asked of it.
type fakeTrashDB struct {
	databasetest.BaseMockDB

	targets []*models.PermissionTarget
	repos   map[string]*models.Repository

	// TrashStore behaviour.
	trashRepoSoft  bool
	trashRepoCalls int
	trashRepoBy    string
	items          map[string]*models.TrashItem
	restoreErr     error
	restoreCalls   int
	purgeRes       *models.TrashPurgeResult
	purgeErr       error
	listFilter     models.TrashFilter
	listItems      []*models.TrashItem
	listTotal      int
	purgeCalls     int
	statefulList   bool // ListTrash hides the items PurgeTrash already removed
	regenA, regenB string

	// Observations.
	deleted        bool
	staleRepos     []string
	stalePackument []string
	decrements     []int
	trashedNames   map[string]bool
	createErr      error
}

func (db *fakeTrashDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return db.targets, nil
}

func (db *fakeTrashDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

func (db *fakeTrashDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return db.repos[name], nil
}

func (db *fakeTrashDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	for _, r := range db.repos {
		if r.ID == id {
			return r, nil
		}
	}

	return nil, nil
}

func (db *fakeTrashDB) CreateRepositoryWithMembers(context.Context, *models.Repository, []string) error {
	return db.createErr
}

func (db *fakeTrashDB) DeleteRepository(context.Context, string) error {
	db.deleted = true
	return nil
}

func (db *fakeTrashDB) MarkRepoStale(_ context.Context, repoID string) error {
	db.staleRepos = append(db.staleRepos, repoID)
	return nil
}

func (db *fakeTrashDB) MarkPackumentStale(_ context.Context, pkgID string) error {
	db.stalePackument = append(db.stalePackument, pkgID)
	return nil
}

func (db *fakeTrashDB) DecrementStorageUsage(_ context.Context, _ string, _ int64, artifacts int) error {
	db.decrements = append(db.decrements, artifacts)
	return nil
}

func (db *fakeTrashDB) BlobReferenceCount(context.Context, string) (int, error) { return 1, nil }

func (db *fakeTrashDB) TrashedRepositoryByName(_ context.Context, name string) (*models.TrashItem, error) {
	if db.trashedNames[name] {
		return &models.TrashItem{Kind: models.TrashKindRepository, Name: name}, nil
	}

	return nil, database.ErrTrashNotFound
}

func (db *fakeTrashDB) TrashRows(context.Context, string, models.TrashKind, []string, string) (bool, error) {
	return false, nil
}

func (db *fakeTrashDB) TrashRepository(_ context.Context, _, by string) (bool, error) {
	db.trashRepoCalls++
	db.trashRepoBy = by

	return db.trashRepoSoft, nil
}

func (db *fakeTrashDB) ListTrash(_ context.Context, f models.TrashFilter) ([]*models.TrashItem, int, error) {
	db.listFilter = f

	if db.statefulList {
		left := db.listItems[min(db.purgeCalls, len(db.listItems)):]

		return left[:min(f.Limit, len(left))], len(left), nil
	}

	return db.listItems, db.listTotal, nil
}

func (db *fakeTrashDB) GetTrashItem(_ context.Context, kind models.TrashKind, id string) (*models.TrashItem, error) {
	if it, ok := db.items[string(kind)+"/"+id]; ok {
		return it, nil
	}

	return nil, database.ErrTrashNotFound
}

func (db *fakeTrashDB) RestoreTrash(_ context.Context, kind models.TrashKind, id string) (*models.TrashItem, error) {
	db.restoreCalls++

	if db.restoreErr != nil {
		return nil, db.restoreErr
	}

	if it, ok := db.items[string(kind)+"/"+id]; ok {
		return it, nil
	}

	return nil, database.ErrTrashNotFound
}

func (db *fakeTrashDB) PurgeTrash(context.Context, models.TrashKind, string) (*models.TrashPurgeResult, error) {
	db.purgeCalls++
	return db.purgeRes, db.purgeErr
}

func (db *fakeTrashDB) RegenKeys(context.Context, models.TrashKind, string) (string, string, error) {
	return db.regenA, db.regenB, nil
}

func (db *fakeTrashDB) ListExpiredTrash(context.Context, int) ([]models.TrashRef, error) {
	return nil, nil
}

var _ database.TrashStore = (*fakeTrashDB)(nil)

type regenCall struct{ kind, repoID, a, b string }

type fakeRegen struct {
	mu    sync.Mutex
	calls []regenCall
}

func (f *fakeRegen) add(c regenCall) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, c)
}

func (f *fakeRegen) NotifyDebStale(repoID string) { f.add(regenCall{"deb", repoID, "", ""}) }
func (f *fakeRegen) NotifyRPMStale(repoID string) { f.add(regenCall{"rpm", repoID, "", ""}) }
func (f *fakeRegen) NotifyMavenStale(repoID, g, a string) {
	f.add(regenCall{"maven", repoID, g, a})
}
func (f *fakeRegen) NotifyNPMStale(pkgID string) { f.add(regenCall{"npm", "", pkgID, ""}) }

func adminOnlyTargets() []*models.PermissionTarget {
	return []*models.PermissionTarget{{RepositoryFilter: `.*`, PathPattern: `.*`, Actions: []string{"read", "write", "delete", "manage"}}}
}

func deleteOnAppTargets() []*models.PermissionTarget {
	return []*models.PermissionTarget{{RepositoryFilter: `^app$`, PathPattern: `.*`, Actions: []string{"read", "delete"}}}
}

func newTrashFixture(t *testing.T, targets []*models.PermissionTarget) (*fakeTrashDB, *fakeRegen, *pubRecorder, chi.Router) {
	t.Helper()

	db := &fakeTrashDB{
		targets: targets,
		repos: map[string]*models.Repository{
			"app":   {ID: trashRepoID, Name: "app", Format: "maven", RepoType: "hosted"},
			"other": {ID: otherRepoID, Name: "other", Format: "maven", RepoType: "hosted"},
		},
		items: map[string]*models.TrashItem{},
	}
	log := newFixTestLogger(t)
	regen := &fakeRegen{}
	pub := &pubRecorder{}

	h := NewTrashHandler(db, log, auth.NewEvaluator(db, log, false)).WithPublisher(pub).WithRegenerator(regen)
	r := chi.NewRouter()
	h.RegisterAuthed(r)

	return db, regen, pub, r
}

type pubRecorder struct {
	mu     sync.Mutex
	events []string
}

func (p *pubRecorder) Dispatch(_ context.Context, _, eventType string, _ map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.events = append(p.events, eventType)
}

func (p *pubRecorder) names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.events...)
}

func trashRequest(method, target, body string) *http.Request {
	userID := "user-1"
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))

	return req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{UserID: &userID, Username: "tester"}))
}

func serve(r http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	return rw
}

func errorCode(t *testing.T, rw *httptest.ResponseRecorder) string {
	t.Helper()

	var body ErrorResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rw.Body.String(), err)
	}

	return body.Code
}

func mavenItem(repoID, repoName string) *models.TrashItem {
	return &models.TrashItem{
		ID: trashItemID, Kind: models.TrashKindMaven, RepositoryID: repoID, RepositoryName: repoName,
		Format: "maven", Name: trashPkgName, Version: "1.0", SizeBytes: 42,
		DeletedAt: time.Unix(1_700_000_000, 0), DeletedBy: "alice", PurgeAt: time.Unix(1_700_600_000, 0),
	}
}

func TestTrashListShapeAndPaging(t *testing.T) {
	db, _, _, r := newTrashFixture(t, adminOnlyTargets())
	db.listItems = []*models.TrashItem{mavenItem(trashRepoID, "app")}
	db.listTotal = 1

	rw := serve(r, trashRequest(http.MethodGet, "/api/v1/trash?limit=9999&page=0&kind=maven_artifact", ""))
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if got["total"] != float64(1) || got["page"] != float64(1) || got["limit"] != float64(200) {
		t.Errorf("list envelope = %v, want total 1 page 1 limit 200", got)
	}

	item := got["items"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "kind", "repository_id", "repository", "format", "name", "version", "size_bytes", "deleted_at", "deleted_by", "purge_at"} {
		if _, ok := item[k]; !ok {
			t.Errorf("item is missing %q: %v", k, item)
		}
	}

	if item["repository"] != "app" || item["kind"] != "maven_artifact" || item["deleted_at"] != "2023-11-14T22:13:20Z" {
		t.Errorf("unexpected item: %v", item)
	}

	if !db.listFilter.IncludeRepositories || db.listFilter.Kind != models.TrashKindMaven || db.listFilter.Limit != 200 || db.listFilter.Page != 1 {
		t.Errorf("filter = %+v", db.listFilter)
	}
}

func TestTrashGlobalRoutesAreAdminOnly(t *testing.T) {
	_, _, _, r := newTrashFixture(t, deleteOnAppTargets()) //nolint:dogsled // only the router matters here

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/trash"},
		{http.MethodPost, "/api/v1/trash/maven_artifact/" + trashItemID + "/restore"},
		{http.MethodDelete, "/api/v1/trash/maven_artifact/" + trashItemID},
		{http.MethodDelete, "/api/v1/trash/repository/" + trashRepoID},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if rw := serve(r, trashRequest(tc.method, tc.path, "")); rw.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", rw.Code, rw.Body.String())
			}
		})
	}
}

func TestTrashRepoRoutesNeedRepoDeletePermission(t *testing.T) {
	db, _, _, r := newTrashFixture(t, deleteOnAppTargets())
	db.items["maven_artifact/"+trashItemID] = mavenItem(trashRepoID, "app")

	if rw := serve(r, trashRequest(http.MethodGet, "/api/v1/repositories/app/trash", "")); rw.Code != http.StatusOK {
		t.Fatalf("list app: status = %d; body = %s", rw.Code, rw.Body.String())
	}

	if db.listFilter.RepositoryID != trashRepoID || db.listFilter.IncludeRepositories {
		t.Errorf("per-repo filter = %+v, want the repo id and no repositories", db.listFilter)
	}

	if rw := serve(r, trashRequest(http.MethodGet, "/api/v1/repositories/other/trash", "")); rw.Code != http.StatusForbidden {
		t.Fatalf("list other: status = %d, want 403", rw.Code)
	}

	if rw := serve(r, trashRequest(http.MethodPost, "/api/v1/repositories/other/trash/maven_artifact/"+trashItemID+"/restore", "")); rw.Code != http.StatusForbidden {
		t.Fatalf("restore in other: status = %d, want 403", rw.Code)
	}

	anon := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/repositories/app/trash", nil)
	anon = anon.WithContext(middleware.WithPrincipal(anon.Context(), &auth.Principal{IsAnonymous: true}))

	if rw := serve(r, anon); rw.Code != http.StatusForbidden {
		t.Fatalf("anonymous: status = %d, want 403", rw.Code)
	}
}

func TestTrashRepoRestoreScoping(t *testing.T) {
	db, regen, pub, r := newTrashFixture(t, adminOnlyTargets())
	db.items["maven_artifact/"+trashItemID] = mavenItem(trashRepoID, "app")
	db.items["repository/"+trashRepoID] = &models.TrashItem{ID: trashRepoID, Kind: models.TrashKindRepository, RepositoryID: trashRepoID, RepositoryName: "app", Name: "app"}
	db.regenA, db.regenB = "com.acme", "lib"

	// An item of another repository must not be restorable through this one.
	if rw := serve(r, trashRequest(http.MethodPost, "/api/v1/repositories/other/trash/maven_artifact/"+trashItemID+"/restore", "")); rw.Code != http.StatusNotFound {
		t.Fatalf("foreign item: status = %d, want 404; body = %s", rw.Code, rw.Body.String())
	}

	if db.restoreCalls != 0 {
		t.Fatalf("RestoreTrash was called for a foreign item")
	}

	// Repositories are not restorable from a repository route.
	if rw := serve(r, trashRequest(http.MethodPost, "/api/v1/repositories/app/trash/repository/"+trashRepoID+"/restore", "")); rw.Code != http.StatusForbidden {
		t.Fatalf("repository kind: status = %d, want 403", rw.Code)
	}

	rw := serve(r, trashRequest(http.MethodPost, "/api/v1/repositories/app/trash/maven_artifact/"+trashItemID+"/restore", ""))
	if rw.Code != http.StatusOK {
		t.Fatalf("restore: status = %d; body = %s", rw.Code, rw.Body.String())
	}

	if got := pub.names(); len(got) != 1 || got[0] != "artifact.restored" {
		t.Errorf("events = %v, want [artifact.restored]", got)
	}

	if len(regen.calls) != 1 || regen.calls[0] != (regenCall{"maven", trashRepoID, "com.acme", "lib"}) {
		t.Errorf("regen calls = %+v", regen.calls)
	}
}

func TestTrashRestoreErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
	}{
		{"not found", database.ErrTrashNotFound, http.StatusNotFound, "not_found"},
		{"conflict", database.ErrTrashConflict, http.StatusConflict, "trash_conflict"},
		{"repo trashed", database.ErrTrashRepoTrashed, http.StatusConflict, "repository_in_trash"},
		{"manifest trashed", database.ErrTrashManifestInTrash, http.StatusConflict, "manifest_in_trash"},
		{"unknown kind", database.ErrTrashUnknownKind, http.StatusBadRequest, "validation_failed"},
		{"other", errors.New("boom: secret detail"), http.StatusInternalServerError, "internal_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, pub, r := newTrashFixture(t, adminOnlyTargets())
			db.restoreErr = tc.err

			rw := serve(r, trashRequest(http.MethodPost, "/api/v1/trash/maven_artifact/"+trashItemID+"/restore", ""))
			if rw.Code != tc.wantCode || errorCode(t, rw) != tc.wantErr {
				t.Fatalf("status/code = %d/%s, want %d/%s; body = %s", rw.Code, errorCode(t, rw), tc.wantCode, tc.wantErr, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), "secret detail") {
				t.Errorf("response leaks the internal error: %s", rw.Body.String())
			}

			if len(pub.names()) != 0 {
				t.Errorf("events emitted for a failed restore: %v", pub.names())
			}
		})
	}
}

func TestTrashInvalidIDIsNotFound(t *testing.T) {
	db, _, _, r := newTrashFixture(t, adminOnlyTargets())

	rw := serve(r, trashRequest(http.MethodPost, "/api/v1/trash/maven_artifact/not-a-uuid/restore", ""))
	if rw.Code != http.StatusNotFound || db.restoreCalls != 0 {
		t.Fatalf("status = %d restoreCalls = %d, want 404 and no store call", rw.Code, db.restoreCalls)
	}
}

func TestTrashRestoreRegenByFormat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      models.TrashKind
		wantRegen []regenCall
		wantRepo  int
		wantPack  []string
	}{
		{"deb", models.TrashKindDeb, []regenCall{{"deb", trashRepoID, "", ""}}, 0, nil},
		{"rpm", models.TrashKindRPM, []regenCall{{"rpm", trashRepoID, "", ""}}, 1, nil},
		{"maven", models.TrashKindMaven, []regenCall{{"maven", trashRepoID, "pkg-a", "pkg-b"}}, 0, nil},
		{"npm", models.TrashKindNPM, []regenCall{{"npm", "", "pkg-a", ""}}, 0, []string{"pkg-a"}},
		{"raw has no generated metadata", models.TrashKindRaw, nil, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, regen, _, r := newTrashFixture(t, adminOnlyTargets())
			it := mavenItem(trashRepoID, "app")
			it.Kind = tc.kind
			db.items[string(tc.kind)+"/"+trashItemID] = it
			db.regenA, db.regenB = "pkg-a", "pkg-b"

			rw := serve(r, trashRequest(http.MethodPost, "/api/v1/trash/"+string(tc.kind)+"/"+trashItemID+"/restore", ""))
			if rw.Code != http.StatusOK {
				t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
			}

			if len(regen.calls) != len(tc.wantRegen) || (len(tc.wantRegen) > 0 && regen.calls[0] != tc.wantRegen[0]) {
				t.Errorf("regen calls = %+v, want %+v", regen.calls, tc.wantRegen)
			}

			if len(db.staleRepos) != tc.wantRepo {
				t.Errorf("MarkRepoStale calls = %v, want %d", db.staleRepos, tc.wantRepo)
			}

			if strings.Join(db.stalePackument, ",") != strings.Join(tc.wantPack, ",") {
				t.Errorf("MarkPackumentStale calls = %v, want %v", db.stalePackument, tc.wantPack)
			}
		})
	}
}

func TestTrashRestoreRepositoryEmitsRepoEvent(t *testing.T) {
	db, regen, pub, r := newTrashFixture(t, adminOnlyTargets())
	db.items["repository/"+trashRepoID] = &models.TrashItem{ID: trashRepoID, Kind: models.TrashKindRepository, RepositoryID: trashRepoID, RepositoryName: "app", Format: "maven", Name: "app"}

	rw := serve(r, trashRequest(http.MethodPost, "/api/v1/trash/repository/"+trashRepoID+"/restore", ""))
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}

	if got := pub.names(); len(got) != 1 || got[0] != "repository.restored" {
		t.Errorf("events = %v, want [repository.restored]", got)
	}

	if len(regen.calls) != 0 {
		t.Errorf("repository restore must not regenerate metadata: %+v", regen.calls)
	}
}

func TestTrashPurgeItemReleasesQuotaAndEmits(t *testing.T) {
	db, _, pub, r := newTrashFixture(t, adminOnlyTargets())
	db.purgeRes = &models.TrashPurgeResult{
		Kind: models.TrashKindMaven, ID: trashItemID, RepositoryID: trashRepoID, RepositoryName: "app",
		Format: "maven", Name: trashPkgName, Version: "1.0", SizeBytes: 42, QuotaArtifacts: 1, QuotaBytes: 42,
	}

	rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/trash/maven_artifact/"+trashItemID, ""))
	if rw.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}

	if len(db.decrements) != 1 || db.decrements[0] != 1 {
		t.Errorf("quota decrements = %v, want [1]", db.decrements)
	}

	if got := pub.names(); len(got) != 1 || got[0] != "artifact.purged" {
		t.Errorf("events = %v, want [artifact.purged]", got)
	}
}

func TestTrashPurgeErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"not found", database.ErrTrashNotFound, http.StatusNotFound},
		{"referenced repository", &pgconn.PgError{Code: "23503"}, http.StatusConflict},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, pub, r := newTrashFixture(t, adminOnlyTargets())
			db.purgeErr = tc.err

			rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/trash/repository/"+trashRepoID, ""))
			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tc.want, rw.Body.String())
			}

			if len(pub.names()) != 0 {
				t.Errorf("events emitted for a failed purge: %v", pub.names())
			}
		})
	}
}

func TestTrashPurgeRepositoryEmitsRepoEvent(t *testing.T) {
	db, _, pub, r := newTrashFixture(t, adminOnlyTargets())
	db.purgeRes = &models.TrashPurgeResult{Kind: models.TrashKindRepository, ID: trashRepoID, RepositoryID: trashRepoID, RepositoryName: "app", Format: "maven", Name: "app"}

	rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/trash/repository/"+trashRepoID, ""))
	if rw.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
	}

	if got := pub.names(); len(got) != 1 || got[0] != "repository.purged" {
		t.Errorf("events = %v, want [repository.purged]", got)
	}
}

func newRepoDeleteFixture(t *testing.T, db *fakeTrashDB) (*RepositoriesHandler, chi.Router, *pubRecorder) {
	t.Helper()

	log := newFixTestLogger(t)
	pub := &pubRecorder{}
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithPublisher(pub)
	r := chi.NewRouter()
	r.Delete("/api/v1/repository/{name}", h.handleDelete)

	return h, r, pub
}

func TestRepositoryDeleteRequiresConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		body   string
		want   int
	}{
		{"missing", "/api/v1/repository/app", "", http.StatusBadRequest},
		{"wrong query", "/api/v1/repository/app?confirm=other", "", http.StatusBadRequest},
		{"wrong body", "/api/v1/repository/app", `{"confirm":"other"}`, http.StatusBadRequest},
		{"empty body object", "/api/v1/repository/app", `{}`, http.StatusBadRequest},
		{"malformed body", "/api/v1/repository/app", `{`, http.StatusBadRequest},
		{"case matters", "/api/v1/repository/app?confirm=APP", "", http.StatusBadRequest},
		{"query ok", "/api/v1/repository/app?confirm=app", "", http.StatusNoContent},
		{"body ok", "/api/v1/repository/app", `{"confirm":"app"}`, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeTrashDB{
				targets:       adminOnlyTargets(),
				repos:         map[string]*models.Repository{"app": {ID: trashRepoID, Name: "app", Format: "raw", RepoType: "hosted"}},
				trashRepoSoft: true,
			}
			_, r, _ := newRepoDeleteFixture(t, db)

			rw := serve(r, trashRequest(http.MethodDelete, tc.target, tc.body))
			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tc.want, rw.Body.String())
			}

			if tc.want == http.StatusBadRequest {
				if code := errorCode(t, rw); code != "confirmation_required" {
					t.Errorf("error code = %q, want confirmation_required", code)
				}

				if db.trashRepoCalls != 0 || db.deleted {
					t.Errorf("repository was touched without confirmation (trash calls %d, deleted %v)", db.trashRepoCalls, db.deleted)
				}
			}
		})
	}
}

func TestRepositoryDeleteSoftAndHardPaths(t *testing.T) {
	for _, soft := range []bool{true, false} {
		db := &fakeTrashDB{
			targets:       adminOnlyTargets(),
			repos:         map[string]*models.Repository{"app": {ID: trashRepoID, Name: "app", Format: "raw", RepoType: "hosted"}},
			trashRepoSoft: soft,
		}
		_, r, pub := newRepoDeleteFixture(t, db)

		rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/repository/app?confirm=app", ""))
		if rw.Code != http.StatusNoContent {
			t.Fatalf("soft=%v: status = %d; body = %s", soft, rw.Code, rw.Body.String())
		}

		if db.trashRepoCalls != 1 || db.trashRepoBy != "tester" {
			t.Errorf("soft=%v: TrashRepository calls = %d by %q, want 1 by tester", soft, db.trashRepoCalls, db.trashRepoBy)
		}

		if db.deleted == soft {
			t.Errorf("soft=%v: hard delete ran = %v, want %v", soft, db.deleted, !soft)
		}

		if got := pub.names(); len(got) != 1 || got[0] != "repository.deleted" {
			t.Errorf("soft=%v: events = %v, want [repository.deleted]", soft, got)
		}
	}
}

func TestRepositoryDeleteNonAdminForbidden(t *testing.T) {
	db := &fakeTrashDB{
		targets: deleteOnAppTargets(),
		repos:   map[string]*models.Repository{"app": {ID: trashRepoID, Name: "app"}},
	}
	_, r, _ := newRepoDeleteFixture(t, db)

	if rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/repository/app?confirm=app", "")); rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rw.Code)
	}
}

func TestRepositoryCreateNameReservedByTrash(t *testing.T) {
	db := &fakeTrashDB{
		targets:      adminOnlyTargets(),
		createErr:    &pgconn.PgError{Code: "23505"},
		trashedNames: map[string]bool{"gone": true},
	}
	log := newFixTestLogger(t)
	h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

	rw := httptest.NewRecorder()
	h.handleCreate(rw, trashRequest(http.MethodPost, "/api/v1/repositories", `{"name":"gone","format":"raw"}`))

	if rw.Code != http.StatusConflict || errorCode(t, rw) != "name_reserved" || !strings.Contains(rw.Body.String(), "trash") {
		t.Fatalf("trashed name: status = %d, body = %s", rw.Code, rw.Body.String())
	}

	rw = httptest.NewRecorder()
	h.handleCreate(rw, trashRequest(http.MethodPost, "/api/v1/repositories", `{"name":"live","format":"raw"}`))

	if rw.Code != http.StatusConflict || errorCode(t, rw) != "conflict" {
		t.Fatalf("live name: status = %d, body = %s", rw.Code, rw.Body.String())
	}
}

func TestRepositoryCreateTrashFields(t *testing.T) {
	log := newFixTestLogger(t)

	for _, tc := range []struct {
		name      string
		body      string
		wantCode  int
		wantDays  *int
		wantImmut bool
	}{
		{"defaults", `{"name":"a","format":"raw"}`, http.StatusCreated, nil, false},
		{"explicit", `{"name":"a","format":"raw","trash_retention_days":7,"immutable_releases":true}`, http.StatusCreated, new(7), true},
		{"zero means no trash", `{"name":"a","format":"raw","trash_retention_days":0}`, http.StatusCreated, new(0), false},
		{"null is default", `{"name":"a","format":"raw","trash_retention_days":null}`, http.StatusCreated, nil, false},
		{"negative", `{"name":"a","format":"raw","trash_retention_days":-1}`, http.StatusBadRequest, nil, false},
		{"too large", `{"name":"a","format":"raw","trash_retention_days":36501}`, http.StatusBadRequest, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeTrashDB{targets: adminOnlyTargets()}
			created := &models.Repository{}
			capture := &captureCreateDB{fakeTrashDB: db, created: created}
			h := NewRepositoriesHandler(capture, log, auth.NewEvaluator(capture, log, false))

			rw := httptest.NewRecorder()
			h.handleCreate(rw, trashRequest(http.MethodPost, "/api/v1/repositories", tc.body))

			if rw.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tc.wantCode, rw.Body.String())
			}

			if tc.wantCode != http.StatusCreated {
				return
			}

			if (created.TrashRetentionDays == nil) != (tc.wantDays == nil) || (tc.wantDays != nil && *created.TrashRetentionDays != *tc.wantDays) {
				t.Errorf("TrashRetentionDays = %v, want %v", created.TrashRetentionDays, tc.wantDays)
			}

			if created.ImmutableReleases != tc.wantImmut {
				t.Errorf("ImmutableReleases = %v, want %v", created.ImmutableReleases, tc.wantImmut)
			}

			var resp map[string]any
			if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}

			if _, ok := resp["trash_retention_days"]; !ok {
				t.Errorf("response lacks trash_retention_days: %v", resp)
			}

			if resp["immutable_releases"] != tc.wantImmut {
				t.Errorf("response immutable_releases = %v, want %v", resp["immutable_releases"], tc.wantImmut)
			}
		})
	}
}

// captureCreateDB records the repository handed to CreateRepositoryWithMembers.
type captureCreateDB struct {
	*fakeTrashDB

	created *models.Repository
}

func (c *captureCreateDB) CreateRepositoryWithMembers(_ context.Context, repo *models.Repository, _ []string) error {
	*c.created = *repo
	repo.ID = "new-id"

	return nil
}

func TestNullableIntDistinguishesAbsentNullAndNumber(t *testing.T) {
	var body struct {
		Days nullableInt `json:"days"`
	}

	for _, tc := range []struct {
		raw     string
		wantSet bool
		wantVal *int
		wantErr bool
	}{
		{`{}`, false, nil, false},
		{`{"days":null}`, true, nil, false},
		{`{"days":0}`, true, new(0), false},
		{`{"days":30}`, true, new(30), false},
		{`{"days":"x"}`, true, nil, true},
	} {
		body.Days = nullableInt{}

		err := json.Unmarshal([]byte(tc.raw), &body)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}

		if tc.wantErr {
			continue
		}

		if body.Days.Set != tc.wantSet || (body.Days.Value == nil) != (tc.wantVal == nil) || (tc.wantVal != nil && *body.Days.Value != *tc.wantVal) {
			t.Errorf("%s: got %+v, want set=%v value=%v", tc.raw, body.Days, tc.wantSet, tc.wantVal)
		}
	}
}

func TestRepositoryUpdateTrashFieldsTriState(t *testing.T) {
	log := newFixTestLogger(t)

	for _, tc := range []struct {
		name      string
		start     *int
		body      string
		want      *int
		wantImmut bool
		wantCode  int
	}{
		{"absent leaves unchanged", new(5), `{"description":"x"}`, new(5), false, http.StatusOK},
		{"null resets to default", new(5), `{"trash_retention_days":null}`, nil, false, http.StatusOK},
		{"number sets", nil, `{"trash_retention_days":0,"immutable_releases":true}`, new(0), true, http.StatusOK},
		{"out of range", new(5), `{"trash_retention_days":40000}`, new(5), false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeTrashDB{
				targets: adminOnlyTargets(),
				repos:   map[string]*models.Repository{"app": {ID: trashRepoID, Name: "app", Format: "raw", RepoType: "hosted", TrashRetentionDays: tc.start}},
			}
			capture := &captureUpdateDB{fakeTrashDB: db}
			h := NewRepositoriesHandler(capture, log, auth.NewEvaluator(capture, log, false))
			r := chi.NewRouter()
			r.Patch("/api/v1/repository/{name}", h.handleUpdate)

			rw := serve(r, trashRequest(http.MethodPatch, "/api/v1/repository/app", tc.body))
			if rw.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rw.Code, tc.wantCode, rw.Body.String())
			}

			if tc.wantCode != http.StatusOK {
				return
			}

			got := capture.updated
			if got == nil || (got.TrashRetentionDays == nil) != (tc.want == nil) || (tc.want != nil && *got.TrashRetentionDays != *tc.want) {
				t.Errorf("updated TrashRetentionDays = %v, want %v", got, tc.want)
			}

			if got.ImmutableReleases != tc.wantImmut {
				t.Errorf("updated ImmutableReleases = %v, want %v", got.ImmutableReleases, tc.wantImmut)
			}
		})
	}
}

type captureUpdateDB struct {
	*fakeTrashDB

	updated *models.Repository
}

func (c *captureUpdateDB) UpdateRepository(_ context.Context, repo *models.Repository) error {
	cp := *repo
	c.updated = &cp

	return nil
}

func (c *captureUpdateDB) GetOrganizationByID(context.Context, string) (*models.Project, error) {
	return nil, nil
}

func TestTrashEmptyRepo(t *testing.T) {
	setup := func(targets []*models.PermissionTarget) (*fakeTrashDB, *pubRecorder, chi.Router) {
		db, _, pub, r := newTrashFixture(t, targets)
		db.statefulList = true
		db.listItems = []*models.TrashItem{mavenItem(trashRepoID, "app"), mavenItem(trashRepoID, "app"), mavenItem(trashRepoID, "app")}
		db.purgeRes = &models.TrashPurgeResult{
			Kind: models.TrashKindMaven, ID: trashItemID, RepositoryID: trashRepoID, RepositoryName: "app",
			Format: "maven", Name: trashPkgName, Version: "1.0", SizeBytes: 42, QuotaArtifacts: 1, QuotaBytes: 42,
		}

		return db, pub, r
	}

	t.Run("repo manage purges everything", func(t *testing.T) {
		manageApp := []*models.PermissionTarget{{RepositoryFilter: `^app$`, PathPattern: `.*`, Actions: []string{"manage"}}}
		db, pub, r := setup(manageApp)

		rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/repositories/app/trash", ""))
		if rw.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", rw.Code, rw.Body.String())
		}

		var body responses.TrashEmptyResponse
		if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}

		if body.Purged != 3 || body.Remaining != 0 {
			t.Errorf("body = %+v, want purged 3 remaining 0", body)
		}

		if db.listFilter.RepositoryID != trashRepoID || db.listFilter.IncludeRepositories {
			t.Errorf("list filter = %+v, want items of the repository only", db.listFilter)
		}

		if len(db.decrements) != 3 {
			t.Errorf("quota decrements = %v, want 3", db.decrements)
		}

		if got := pub.names(); len(got) != 3 || got[0] != "artifact.purged" {
			t.Errorf("events = %v, want 3x artifact.purged", got)
		}
	})

	t.Run("admin purges everything", func(t *testing.T) {
		db, _, r := setup(adminOnlyTargets())

		rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/repositories/app/trash", ""))
		if rw.Code != http.StatusOK || db.purgeCalls != 3 {
			t.Fatalf("status = %d purgeCalls = %d; body = %s", rw.Code, db.purgeCalls, rw.Body.String())
		}
	})

	t.Run("repo delete permission is not enough", func(t *testing.T) {
		db, _, r := setup(deleteOnAppTargets())

		rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/repositories/app/trash", ""))
		if rw.Code != http.StatusForbidden || db.purgeCalls != 0 {
			t.Fatalf("status = %d purgeCalls = %d", rw.Code, db.purgeCalls)
		}
	})

	t.Run("manage on another repo is forbidden", func(t *testing.T) {
		manageOther := []*models.PermissionTarget{{RepositoryFilter: `^other$`, PathPattern: `.*`, Actions: []string{"manage"}}}
		db, _, r := setup(manageOther)

		rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/repositories/app/trash", ""))
		if rw.Code != http.StatusForbidden || db.purgeCalls != 0 {
			t.Fatalf("status = %d purgeCalls = %d", rw.Code, db.purgeCalls)
		}
	})

	t.Run("anonymous is forbidden", func(t *testing.T) {
		db, _, r := setup(adminOnlyTargets())
		req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/v1/repositories/app/trash", nil)

		rw := serve(r, req)
		if rw.Code != http.StatusForbidden || db.purgeCalls != 0 {
			t.Fatalf("status = %d purgeCalls = %d", rw.Code, db.purgeCalls)
		}
	})

	t.Run("unknown repository is 404", func(t *testing.T) {
		_, _, r := setup(adminOnlyTargets())

		rw := serve(r, trashRequest(http.MethodDelete, "/api/v1/repositories/nope/trash", ""))
		if rw.Code != http.StatusNotFound {
			t.Fatalf("status = %d", rw.Code)
		}
	})
}
