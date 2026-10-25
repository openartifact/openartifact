// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/worker"
)

// These tests drive the real maven/raw handlers, the repositories and trash
// API handlers and the purge worker over the harness' Postgres and filesystem
// blob store, and assert on HTTP status codes, served bytes, listings, the
// blob store and the audit log: the retention, deletion-protection, quota and
// audit policies of the trash (docs/operations/trash.md).

const (
	// tpoUserHeader selects the principal a test request is sent as.
	tpoUserHeader = "X-Tpo-User"
	// tpoAdmin is the global `manage` principal (the legacy admin credential).
	tpoAdmin = "admin"
	// tpoWriter is a regular user with read/write/delete on every repository
	// and no `manage`.
	tpoWriter = "writer"
	// tpoAdminName is the username the admin principal carries (audit actor).
	tpoAdminName = "tpo-admin"
)

// tpoEnv is the HTTP surface of one test: the real handlers behind a router
// that attaches the principal named by tpoUserHeader.
type tpoEnv struct {
	router chi.Router
	repos  *api.RepositoriesHandler
	trash  *api.TrashHandler
	writer *auth.Principal
}

// tpoNewEnv resets the shared harness, turns the trash on with the default
// 7 day retention (the harness database defaults to 0 = trash off) and wires
// the handlers. The trash default and the evaluator are restored afterwards.
func tpoNewEnv(t *testing.T) *tpoEnv {
	t.Helper()
	harness.Reset(t)

	pg, ok := harness.Database.(*database.PostgresDB)
	if !ok {
		t.Fatalf("harness database is %T, want *database.PostgresDB", harness.Database)
	}

	pg.SetTrashRetentionDays(7)
	t.Cleanup(func() { pg.SetTrashRetentionDays(0) })

	evaluator := auth.NewEvaluator(pg, harness.Log, false)

	trash.SetEvaluator(evaluator)
	t.Cleanup(func() { trash.SetEvaluator(nil) })

	writer := tpoCreateWriter(t, pg)
	principals := map[string]*auth.Principal{
		tpoAdmin:  {Username: tpoAdminName, IsLegacy: true},
		tpoWriter: writer,
	}

	router := chi.NewRouter()
	router.Use(tpoPrincipalMiddleware(principals))

	// Detached orphan sweeps must not outlive the test: the next test resets
	// the tables they read. Shutdown cancels and awaits them.
	tasks := bgtask.New(t.Context())
	t.Cleanup(func() { tasks.Shutdown(time.Minute) })

	env := &tpoEnv{
		router: router,
		repos:  api.NewRepositoriesHandler(pg, harness.Log, evaluator).WithBlobStore(harness.BlobStore).WithTasks(tasks),
		trash:  api.NewTrashHandler(pg, harness.Log, evaluator).WithBlobStore(harness.BlobStore).WithTasks(tasks),
		writer: writer,
	}

	env.repos.RegisterAuthed(router)
	env.trash.RegisterAuthed(router)

	// Same order as cmd/server: the format subrouter is mounted first, the
	// upload handler then takes over PUT and DELETE on the same prefix.
	mavenHTTP := formatmaven.NewHTTPHandler(harness.Log, pg, harness.BlobStore)
	router.Route("/repos/maven/{name}", func(r chi.Router) { mavenHTTP.RegisterRepoRoutes(r) })
	api.NewMavenUploadHandler(harness.Log, pg, harness.BlobStore, nil).Register(router)

	rawHTTP := formatraw.NewHTTPHandler(harness.Log, pg, harness.BlobStore, "")
	router.Route("/repos/raw/{name}", func(r chi.Router) { rawHTTP.RegisterRepoRoutes(r) })

	return env
}

func tpoPrincipalMiddleware(principals map[string]*auth.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := principals[r.Header.Get(tpoUserHeader)]; ok {
				r = r.WithContext(middleware.WithPrincipal(r.Context(), p))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// tpoCreateWriter creates a real user holding read, write and delete on every
// repository (and nothing else) and returns its principal.
func tpoCreateWriter(t *testing.T, pg *database.PostgresDB) *auth.Principal {
	t.Helper()

	name := "tpo-writer-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	user, err := pg.CreateUser(t.Context(), &models.User{Username: name})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	target, err := pg.CreatePermissionTarget(t.Context(), &models.PermissionTarget{
		Name: name, RepositoryFilter: ".*", PathPattern: ".*", Actions: []string{"read", "write", "delete"},
	})
	if err != nil {
		t.Fatalf("CreatePermissionTarget: %v", err)
	}

	assignment := &models.PermissionAssignment{PermissionTargetID: target.ID, PrincipalType: "user", PrincipalID: user.ID}
	if err := pg.CreatePermissionAssignment(t.Context(), assignment); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	t.Cleanup(func() {
		_, _ = harness.DB.ExecContext(context.Background(), `DELETE FROM permission_targets WHERE id = $1`, target.ID)
		_, _ = harness.DB.ExecContext(context.Background(), `DELETE FROM auth_users WHERE id = $1`, user.ID)
	})

	id := user.ID

	return &auth.Principal{UserID: &id, Username: name, Credential: auth.CredentialSession}
}

// tpoSend runs one request through the router as who ("" = anonymous).
func tpoSend(t *testing.T, env *tpoEnv, who, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, target, reader)
	if who != "" {
		req.Header.Set(tpoUserHeader, who)
	}

	if strings.HasPrefix(target, "/api/") && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	return rec
}

func tpoExpect(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d: %s", what, rec.Code, want, rec.Body.String())
	}
}

// tpoExpectError checks the status and the machine-readable error code of an
// API error response.
func tpoExpectError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, what string) {
	t.Helper()
	tpoExpect(t, rec, status, what)

	if got := tpoDecode[api.ErrorResponse](t, rec).Code; got != code {
		t.Errorf("%s: error code %q, want %q", what, got, code)
	}
}

// tpoExpectImmutable checks a 409 produced by the immutable-releases guard
// (as opposed to any other conflict).
func tpoExpectImmutable(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	tpoExpect(t, rec, http.StatusConflict, what)

	if !strings.Contains(rec.Body.String(), "immutable") {
		t.Errorf("%s: 409 body %q does not name immutable releases", what, rec.Body.String())
	}
}

func tpoDecode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}

	return out
}

func tpoContent(label string) []byte {
	return []byte("tpo-" + label + "-" + strconv.FormatInt(time.Now().UnixNano(), 10))
}

func tpoSHA(content []byte) string {
	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

func tpoMavenPath(artifact, version string) string {
	return "com/acme/" + artifact + "/" + version + "/" + artifact + "-" + version + ".jar"
}

func tpoMavenURL(repo, path string) string { return "/repos/maven/" + repo + "/" + path }

func tpoRawURL(repo, path string) string { return "/repos/raw/" + repo + "/" + path }

// tpoCreateRepo creates a repository through the real create endpoint (as admin).
func tpoCreateRepo(t *testing.T, env *tpoEnv, name, format string, opts map[string]any) {
	t.Helper()

	body := map[string]any{"name": name, "format": format}
	maps.Copy(body, opts)

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal create body: %v", err)
	}

	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodPost, "/api/v1/repositories", raw), http.StatusCreated, "create repository "+name)
}

func tpoPatchRepo(t *testing.T, env *tpoEnv, name, body string) {
	t.Helper()
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodPatch, "/api/v1/repository/"+name, []byte(body)), http.StatusOK, "patch "+name+" "+body)
}

func tpoRepoInfo(t *testing.T, env *tpoEnv, name string) responses.RepositoryResponse {
	t.Helper()

	rec := tpoSend(t, env, tpoAdmin, http.MethodGet, "/api/v1/repository/"+name, nil)
	tpoExpect(t, rec, http.StatusOK, "get repository "+name)

	return tpoDecode[responses.RepositoryResponse](t, rec)
}

type tpoRepoList struct {
	Repositories []responses.RepositoryResponse `json:"repositories"`
}

func tpoRepoListed(t *testing.T, env *tpoEnv, name string) bool {
	t.Helper()

	rec := tpoSend(t, env, tpoAdmin, http.MethodGet, "/api/v1/repositories?limit=100", nil)
	tpoExpect(t, rec, http.StatusOK, "list repositories")

	return slices.ContainsFunc(tpoDecode[tpoRepoList](t, rec).Repositories, func(r responses.RepositoryResponse) bool {
		return r.Name == name
	})
}

// tpoPut uploads content to a native URL as the writer and checks the status.
func tpoPut(t *testing.T, env *tpoEnv, target string, content []byte, want int) *httptest.ResponseRecorder {
	t.Helper()

	rec := tpoSend(t, env, tpoWriter, http.MethodPut, target, content)
	tpoExpect(t, rec, want, "PUT "+target)

	return rec
}

func tpoAssertServes(t *testing.T, env *tpoEnv, target string, want []byte) {
	t.Helper()

	rec := tpoSend(t, env, tpoWriter, http.MethodGet, target, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("GET %s = %d %q, want 200 %q", target, rec.Code, rec.Body.String(), want)
	}
}

func tpoAssertGone(t *testing.T, env *tpoEnv, target string) {
	t.Helper()

	if rec := tpoSend(t, env, tpoWriter, http.MethodGet, target, nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want 404", target, rec.Code)
	}
}

func tpoTrash(t *testing.T, env *tpoEnv, query string) responses.TrashListResponse {
	t.Helper()

	rec := tpoSend(t, env, tpoAdmin, http.MethodGet, "/api/v1/trash"+query, nil)
	tpoExpect(t, rec, http.StatusOK, "list trash"+query)

	return tpoDecode[responses.TrashListResponse](t, rec)
}

// tpoRepoTrash lists the trash of one repository.
func tpoRepoTrash(t *testing.T, env *tpoEnv, repo string) responses.TrashListResponse {
	t.Helper()

	return tpoTrash(t, env, "?limit=200&repository="+url.QueryEscape(repo))
}

// tpoTrashItem finds the trashed item called name in repo's trash.
func tpoTrashItem(t *testing.T, env *tpoEnv, repo, name string) responses.TrashItemResponse {
	t.Helper()

	for _, it := range tpoRepoTrash(t, env, repo).Items {
		if it.Name == name {
			return it
		}
	}

	t.Fatalf("%q is not in the trash of %s", name, repo)

	return responses.TrashItemResponse{}
}

func tpoCount(t *testing.T, query string, args ...any) int {
	t.Helper()

	var n int
	if err := harness.DB.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}

	return n
}

func tpoExec(t *testing.T, query string, args ...any) {
	t.Helper()

	if _, err := harness.DB.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func tpoRepoID(t *testing.T, name string) string {
	t.Helper()

	var id string
	if err := harness.DB.QueryRowContext(t.Context(), `SELECT id FROM repositories WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("repository %s: %v", name, err)
	}

	return id
}

// tpoAge back-dates every trashed row of repo in table by days, so the
// retention has expired.
func tpoAge(t *testing.T, table, repo string, days int) {
	t.Helper()

	tpoExec(t, `UPDATE `+table+` SET deleted_at = now() - make_interval(days => $2)
		WHERE repository_id = (SELECT id FROM repositories WHERE name = $1) AND deleted_at IS NOT NULL`, repo, days)
}

// tpoCopyDeleter lets the harness' blob store serve as the sweeper's byte store.
type tpoCopyDeleter struct{ storage.BlobStore }

func (c tpoCopyDeleter) DeleteCopy(ctx context.Context, _, sha string) error {
	return c.Delete(ctx, sha)
}

// tpoBlobStored reports whether the bytes of sha are still stored. Deleting a
// blob only queues its bytes, so the sweeper runs first (grace 0, no running
// backup: everything queued is due).
func tpoBlobStored(t *testing.T, sha string) bool {
	t.Helper()

	sweeper := worker.NewBlobDeletionSweeper(harness.Database, tpoCopyDeleter{harness.BlobStore}, harness.Log, 0, 500)
	if _, err := sweeper.RunOnce(t.Context()); err != nil {
		t.Fatalf("sweep queued blob deletions: %v", err)
	}

	_, ok, err := harness.BlobStore.ExistsBySHA256(t.Context(), sha)
	if err != nil {
		t.Fatalf("blob store lookup: %v", err)
	}

	return ok
}

func tpoBlobRows(t *testing.T, sha string) int {
	t.Helper()

	return tpoCount(t, `SELECT COUNT(*) FROM blobs WHERE sha256 = $1`, sha)
}

// tpoAssertBlobFreed waits (bounded) for the blob to leave the store and the
// blobs table: repository deletes and purges free blobs in a detached sweep.
func tpoAssertBlobFreed(t *testing.T, label string, content []byte) {
	t.Helper()

	sha := tpoSHA(content)
	deadline := time.Now().Add(10 * time.Second)

	for tpoBlobStored(t, sha) || tpoBlobRows(t, sha) != 0 {
		if time.Now().After(deadline) {
			t.Errorf("%s: blob still in the blob store or the blobs table, want freed", label)

			return
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func tpoAssertBlobKept(t *testing.T, label string, content []byte) {
	t.Helper()

	sha := tpoSHA(content)
	if !tpoBlobStored(t, sha) || tpoBlobRows(t, sha) != 1 {
		t.Errorf("%s: blob missing from the blob store or the blobs table, want kept", label)
	}
}

// tpoRunPurge runs one pass of the real purge worker over the harness stores.
func tpoRunPurge(t *testing.T) worker.PurgeStats {
	t.Helper()

	w := worker.NewTrashPurgeWorker(harness.Database, harness.BlobStore, harness.Log, 0)
	t.Cleanup(w.Stop)

	stats, err := w.RunOnce(t.Context())
	if err != nil {
		t.Fatalf("purge pass: %v", err)
	}

	return stats
}

func tpoAssertQuota(t *testing.T, repo string, wantBytes int, wantCount int) {
	t.Helper()

	var (
		size  int64
		count int
	)

	if err := harness.DB.QueryRowContext(t.Context(),
		`SELECT q.current_size_bytes, q.current_artifact_count
		   FROM storage_quotas q JOIN repositories r ON r.id = q.repository_id WHERE r.name = $1`, repo,
	).Scan(&size, &count); err != nil {
		t.Fatalf("quota of %s: %v", repo, err)
	}

	if size != int64(wantBytes) || count != wantCount {
		t.Errorf("quota usage of %s = (%d bytes, %d artifacts), want (%d, %d)", repo, size, count, wantBytes, wantCount)
	}
}

// 1. Purge frees blobs ------------------------------------------------------

func TestTrashPolicies_PurgeFreesOnlyUnsharedBlobs(t *testing.T) {
	env := tpoNewEnv(t)

	const repoA, repoB = "tpo-purge-a", "tpo-purge-b"

	tpoCreateRepo(t, env, repoA, "maven", nil)
	tpoCreateRepo(t, env, repoB, "maven", nil)

	unique, shared := tpoContent("unique"), tpoContent("shared")
	uniquePath, sharedPath := tpoMavenPath("lonely", "1.0.0"), tpoMavenPath("shared", "1.0.0")

	tpoPut(t, env, tpoMavenURL(repoA, uniquePath), unique, http.StatusCreated)
	tpoPut(t, env, tpoMavenURL(repoA, sharedPath), shared, http.StatusCreated)
	// The same bytes, live in another repository.
	tpoPut(t, env, tpoMavenURL(repoB, sharedPath), shared, http.StatusCreated)

	for _, path := range []string{uniquePath, sharedPath} {
		tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repoA, path), nil), http.StatusNoContent, "delete "+path)
		tpoAssertGone(t, env, tpoMavenURL(repoA, path))
	}

	if got := tpoRepoTrash(t, env, repoA).Total; got != 2 {
		t.Fatalf("trash of %s holds %d items, want 2", repoA, got)
	}

	// Soft delete keeps every byte.
	tpoAssertBlobKept(t, "unique blob while trashed", unique)
	tpoAssertBlobKept(t, "shared blob while trashed", shared)

	tpoAge(t, "maven_artifacts", repoA, 8)

	stats := tpoRunPurge(t)
	if stats.Purged != 2 || stats.Failed != 0 || stats.BlobErrors != 0 {
		t.Fatalf("purge pass = %+v, want 2 purged without failures", stats)
	}

	if rows := tpoCount(t, `SELECT COUNT(*) FROM maven_artifacts WHERE repository_id = $1`, tpoRepoID(t, repoA)); rows != 0 {
		t.Errorf("%d maven rows of %s survived the purge", rows, repoA)
	}

	tpoAssertBlobFreed(t, "blob only the purged row used", unique)
	tpoAssertBlobKept(t, "blob a live row of another repository still uses", shared)
	tpoAssertServes(t, env, tpoMavenURL(repoB, sharedPath), shared)
}

func TestTrashPolicies_ManualPurgeIsAdminOnlyAndFreesBlob(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-manual-purge"

	tpoCreateRepo(t, env, repo, "maven", nil)

	content, path := tpoContent("manual"), tpoMavenPath("manual", "1.0.0")
	tpoPut(t, env, tpoMavenURL(repo, path), content, http.StatusCreated)
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repo, path), nil), http.StatusNoContent, "delete")

	item := tpoTrashItem(t, env, repo, path)
	if item.Kind != "maven_artifact" || item.DeletedBy != env.writer.Username {
		t.Errorf("trash item = %+v, want a maven_artifact deleted by %s", item, env.writer.Username)
	}

	purge := "/api/v1/trash/" + item.Kind + "/" + item.ID

	tpoExpectError(t, tpoSend(t, env, tpoWriter, http.MethodDelete, purge, nil), http.StatusForbidden, "forbidden", "purge as non-admin")
	tpoExpectError(t, tpoSend(t, env, "", http.MethodDelete, purge, nil), http.StatusForbidden, "forbidden", "purge as anonymous")

	if tpoCount(t, `SELECT COUNT(*) FROM maven_artifacts WHERE id = $1`, item.ID) != 1 {
		t.Fatal("a refused purge removed the row")
	}

	tpoAssertBlobKept(t, "blob after a refused purge", content)

	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, purge, nil), http.StatusNoContent, "purge as admin")

	if tpoCount(t, `SELECT COUNT(*) FROM maven_artifacts WHERE id = $1`, item.ID) != 0 {
		t.Error("the row survived an admin purge")
	}

	tpoAssertBlobFreed(t, "blob of the purged row", content)

	if got := tpoRepoTrash(t, env, repo).Total; got != 0 {
		t.Errorf("trash still lists %d items after the purge", got)
	}

	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, purge, nil), http.StatusNotFound, "purge twice")
}

// 2. Retention ----------------------------------------------------------------

func TestTrashPolicies_RetentionZeroHardDeletes(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-ret-zero"

	tpoCreateRepo(t, env, repo, "maven", map[string]any{"trash_retention_days": 0})

	if days := tpoRepoInfo(t, env, repo).TrashRetentionDays; days == nil || *days != 0 {
		t.Fatalf("trash_retention_days = %v, want 0", days)
	}

	content, path := tpoContent("hard"), tpoMavenPath("app", "1.0.0")
	tpoPut(t, env, tpoMavenURL(repo, path), content, http.StatusCreated)
	tpoAssertServes(t, env, tpoMavenURL(repo, path), content)

	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repo, path), nil), http.StatusNoContent, "delete")

	tpoAssertGone(t, env, tpoMavenURL(repo, path))

	// No row at all, deleted or not.
	if rows := tpoCount(t, `SELECT COUNT(*) FROM maven_artifacts WHERE repository_id = $1`, tpoRepoID(t, repo)); rows != 0 {
		t.Errorf("%d maven rows left after a retention-0 delete, want none", rows)
	}

	tpoAssertBlobFreed(t, "blob of the hard-deleted row", content)

	if got := tpoRepoTrash(t, env, repo); got.Total != 0 || len(got.Items) != 0 {
		t.Errorf("trash of %s = %+v, want empty", repo, got)
	}

	if got := tpoTrash(t, env, ""); got.Total != 0 {
		t.Errorf("global trash holds %d items, want none", got.Total)
	}
}

func TestTrashPolicies_RetentionOverridePerRepository(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-ret-override"

	// No override: the repository follows the server-wide 7 days.
	tpoCreateRepo(t, env, repo, "raw", nil)

	phases := []struct {
		patch    string
		wantDays int
	}{
		{patch: "", wantDays: 7},
		{patch: `{"trash_retention_days":30}`, wantDays: 30},
		{patch: `{"trash_retention_days":0}`, wantDays: 0},
		// null resets the override to the server-wide default.
		{patch: `{"trash_retention_days":null}`, wantDays: 7},
	}

	for i, phase := range phases {
		if phase.patch != "" {
			tpoPatchRepo(t, env, repo, phase.patch)
		}

		file := "phase" + strconv.Itoa(i) + ".txt"
		target := tpoRawURL(repo, file)

		tpoPut(t, env, target, tpoContent(file), http.StatusCreated)
		tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, target, nil), http.StatusNoContent, "delete "+file)
		tpoAssertGone(t, env, target)
		tpoAssertRetention(t, env, repo, file, phase.wantDays)
	}
}

// tpoAssertRetention checks what a delete in repo left of the raw file name:
// nothing when wantDays is 0, else a trash item that is purged wantDays after
// its deletion.
func tpoAssertRetention(t *testing.T, env *tpoEnv, repo, name string, wantDays int) {
	t.Helper()

	rows := tpoCount(t,
		`SELECT COUNT(*) FROM raw_artifacts WHERE path = $1 AND repository_id = (SELECT id FROM repositories WHERE name = $2)`, name, repo)

	if wantDays == 0 {
		if rows != 0 {
			t.Errorf("%s: row survived a retention-0 delete", name)
		}

		if slices.ContainsFunc(tpoRepoTrash(t, env, repo).Items, func(it responses.TrashItemResponse) bool { return it.Name == name }) {
			t.Errorf("%s: listed in the trash although retention is 0", name)
		}

		return
	}

	if rows != 1 {
		t.Errorf("%s: %d rows after a soft delete, want 1", name, rows)
	}

	item := tpoTrashItem(t, env, repo, name)

	deleted, err := time.Parse(time.RFC3339, item.DeletedAt)
	if err != nil {
		t.Fatalf("deleted_at %q: %v", item.DeletedAt, err)
	}

	purgeAt, err := time.Parse(time.RFC3339, item.PurgeAt)
	if err != nil {
		t.Fatalf("purge_at %q: %v", item.PurgeAt, err)
	}

	// Calendar days in the session time zone: tolerate a DST hour.
	want := time.Duration(wantDays) * 24 * time.Hour
	if diff := purgeAt.Sub(deleted) - want; diff > time.Hour || diff < -time.Hour {
		t.Errorf("%s: purge_at is %v after deleted_at, want %d days", name, purgeAt.Sub(deleted), wantDays)
	}
}

// 3. immutable_releases ---------------------------------------------------------

func TestTrashPolicies_ImmutableReleasesMaven(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-imm-maven"

	tpoCreateRepo(t, env, repo, "maven", map[string]any{"immutable_releases": true})

	release, snapshot := tpoMavenPath("app", "1.0.0"), tpoMavenPath("app", "1.1.0-SNAPSHOT")
	releaseURL, snapshotURL := tpoMavenURL(repo, release), tpoMavenURL(repo, snapshot)
	original := tpoContent("release")

	tpoPut(t, env, releaseURL, original, http.StatusCreated)
	tpoPut(t, env, snapshotURL, tpoContent("snapshot-1"), http.StatusCreated)

	// A released version can be neither deleted nor overwritten.
	tpoExpectImmutable(t, tpoSend(t, env, tpoWriter, http.MethodDelete, releaseURL, nil), "delete release")
	tpoAssertServes(t, env, releaseURL, original)

	tpoPut(t, env, releaseURL, tpoContent("release-v2"), http.StatusConflict)
	tpoAssertServes(t, env, releaseURL, original)

	// force is for admins only, and only with the parameter.
	tpoExpectImmutable(t, tpoSend(t, env, tpoWriter, http.MethodDelete, releaseURL+"?force=true", nil), "non-admin force")
	tpoExpectImmutable(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, releaseURL, nil), "admin without force")
	tpoAssertServes(t, env, releaseURL, original)

	// Snapshots stay mutable: overwrite, then delete (soft).
	updated := tpoContent("snapshot-2")
	tpoPut(t, env, snapshotURL, updated, http.StatusOK)
	tpoAssertServes(t, env, snapshotURL, updated)
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, snapshotURL, nil), http.StatusNoContent, "delete snapshot")
	tpoAssertGone(t, env, snapshotURL)

	if item := tpoTrashItem(t, env, repo, snapshot); item.Version != "1.1.0-SNAPSHOT" {
		t.Errorf("trashed snapshot version = %q, want 1.1.0-SNAPSHOT", item.Version)
	}

	// An admin can force one request.
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, releaseURL+"?force=true", nil), http.StatusNoContent, "admin force delete")
	tpoAssertGone(t, env, releaseURL)

	item := tpoTrashItem(t, env, repo, release)
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodPost, "/api/v1/trash/"+item.Kind+"/"+item.ID+"/restore", nil), http.StatusOK, "restore")
	tpoAssertServes(t, env, releaseURL, original)
}

func TestTrashPolicies_ImmutableReleasesRaw(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-imm-raw"

	tpoCreateRepo(t, env, repo, "raw", map[string]any{"immutable_releases": true})

	target := tpoRawURL(repo, "dist/tool-1.0.tgz")
	original := tpoContent("tool")

	tpoPut(t, env, target, original, http.StatusCreated)

	// Every raw file is a release: no overwrite, no delete.
	tpoExpectImmutable(t, tpoSend(t, env, tpoWriter, http.MethodPut, target, tpoContent("tool-v2")), "overwrite")
	tpoExpectImmutable(t, tpoSend(t, env, tpoWriter, http.MethodDelete, target, nil), "delete")
	tpoExpectImmutable(t, tpoSend(t, env, tpoWriter, http.MethodDelete, target+"?force=true", nil), "non-admin force")
	tpoExpectImmutable(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, target, nil), "admin without force")
	tpoAssertServes(t, env, target, original)

	// The setting is what protects: switched off, the overwrite goes through.
	tpoPatchRepo(t, env, repo, `{"immutable_releases":false}`)

	replaced := tpoContent("tool-v2")
	tpoPut(t, env, target, replaced, http.StatusCreated)
	tpoAssertServes(t, env, target, replaced)

	tpoPatchRepo(t, env, repo, `{"immutable_releases":true}`)
	tpoExpectImmutable(t, tpoSend(t, env, tpoWriter, http.MethodPut, target, tpoContent("tool-v3")), "overwrite after re-enabling")

	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, target+"?force=true", nil), http.StatusNoContent, "admin force delete")
	tpoAssertGone(t, env, target)
	tpoTrashItem(t, env, repo, "dist/tool-1.0.tgz")
}

// 4. Repository deletion ---------------------------------------------------------

func TestTrashPolicies_RepositoryDeleteConfirmationAndTrash(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-repo-soft"

	tpoCreateRepo(t, env, repo, "raw", nil)

	files := map[string][]byte{"a.txt": tpoContent("a"), "dir/b.txt": tpoContent("b")}
	for path, content := range files {
		tpoPut(t, env, tpoRawURL(repo, path), content, http.StatusCreated)
	}

	del := "/api/v1/repository/" + repo

	tpoExpectError(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, del, nil), http.StatusBadRequest, "confirmation_required", "delete without confirm")
	tpoExpectError(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, del+"?confirm=not-the-name", nil),
		http.StatusBadRequest, "confirmation_required", "delete with a wrong confirm")
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, del+"?confirm="+repo, nil), http.StatusForbidden, "delete as non-admin")

	if !tpoRepoListed(t, env, repo) {
		t.Fatal("a refused delete removed the repository")
	}

	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, del+"?confirm="+repo, nil), http.StatusNoContent, "delete with confirm")

	// Hidden from the API and the native protocol.
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodGet, del, nil), http.StatusNotFound, "get trashed repository")

	if tpoRepoListed(t, env, repo) {
		t.Error("trashed repository is still listed")
	}

	for path := range files {
		tpoAssertGone(t, env, tpoRawURL(repo, path))
	}

	item := tpoTrashItem(t, env, "", repo)
	if item.Kind != "repository" || item.Name != repo {
		t.Fatalf("trash item = %+v, want the repository %s", item, repo)
	}

	// The name stays reserved while the repository is in the trash.
	create, err := json.Marshal(map[string]string{"name": repo, "format": "raw"})
	if err != nil {
		t.Fatal(err)
	}

	tpoExpectError(t, tpoSend(t, env, tpoAdmin, http.MethodPost, "/api/v1/repositories", create), http.StatusConflict, "name_reserved", "create under a reserved name")

	restore := "/api/v1/trash/repository/" + item.ID + "/restore"

	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodPost, restore, nil), http.StatusForbidden, "restore as non-admin")
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodPost, restore, nil), http.StatusOK, "restore as admin")

	if !tpoRepoListed(t, env, repo) {
		t.Error("restored repository is not listed")
	}

	for path, content := range files {
		tpoAssertServes(t, env, tpoRawURL(repo, path), content)
	}

	if got := tpoTrash(t, env, "").Total; got != 0 {
		t.Errorf("trash holds %d items after the restore, want none", got)
	}

	// Restored: the name is an ordinary conflict again.
	tpoExpectError(t, tpoSend(t, env, tpoAdmin, http.MethodPost, "/api/v1/repositories", create), http.StatusConflict, "conflict", "create under a live name")
}

func TestTrashPolicies_RepositoryRetentionZeroHardDeletes(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-repo-hard"

	tpoCreateRepo(t, env, repo, "raw", map[string]any{"trash_retention_days": 0})

	content := tpoContent("doomed")
	target := tpoRawURL(repo, "doomed.txt")
	tpoPut(t, env, target, content, http.StatusCreated)

	repoID := tpoRepoID(t, repo)
	// Old enough for the orphan sweep a repository delete runs (storage.OrphanMinAge).
	tpoExec(t, `UPDATE blobs SET created_at = now() - interval '2 hours' WHERE sha256 = $1`, tpoSHA(content))

	del := "/api/v1/repository/" + repo

	// The confirmation is required here too.
	tpoExpectError(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, del, nil), http.StatusBadRequest, "confirmation_required", "delete without confirm")
	tpoAssertServes(t, env, target, content)

	body, err := json.Marshal(map[string]string{"confirm": repo})
	if err != nil {
		t.Fatal(err)
	}

	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, del, body), http.StatusNoContent, "delete with a confirm body")

	if n := tpoCount(t, `SELECT COUNT(*) FROM repositories WHERE name = $1`, repo); n != 0 {
		t.Errorf("repository row survived a retention-0 delete (%d rows, trashed or not)", n)
	}

	if n := tpoCount(t, `SELECT COUNT(*) FROM raw_artifacts WHERE repository_id = $1`, repoID); n != 0 {
		t.Errorf("%d raw rows survived the repository delete", n)
	}

	if got := tpoTrash(t, env, "").Total; got != 0 {
		t.Errorf("trash holds %d items, want none: retention 0 deletes for good", got)
	}

	tpoAssertBlobFreed(t, "blob of the deleted repository", content)

	// Nothing reserves the name.
	tpoCreateRepo(t, env, repo, "raw", nil)
}

// 5. Restore conflict ------------------------------------------------------------

// tpoFormatCase is a hosted format the restore-conflict test runs against.
type tpoFormatCase struct {
	format string
	// kind is the trash kind of its rows.
	kind string
	// path is the artifact's path inside the repository, which is also the
	// name of its trash item.
	path string
	// urlPrefix is the native route prefix up to the repository name.
	urlPrefix string
}

func TestTrashPolicies_RestoreConflictWhenPathWasPublishedAgain(t *testing.T) {
	cases := []tpoFormatCase{
		{format: "raw", kind: "raw_artifact", path: "docs/readme.txt", urlPrefix: "/repos/raw/"},
		{format: "maven", kind: "maven_artifact", path: tpoMavenPath("app", "1.0.0"), urlPrefix: "/repos/maven/"},
	}

	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) { tpoRunRestoreConflict(t, tc) })
	}
}

func tpoRunRestoreConflict(t *testing.T, tc tpoFormatCase) {
	t.Helper()

	env := tpoNewEnv(t)
	repo := "tpo-conflict-" + tc.format
	target := tc.urlPrefix + repo + "/" + tc.path
	oldContent, newContent := tpoContent("old"), tpoContent("new")

	tpoCreateRepo(t, env, repo, tc.format, nil)
	tpoPut(t, env, target, oldContent, http.StatusCreated)
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, target, nil), http.StatusNoContent, "delete")

	old := tpoTrashItem(t, env, repo, tc.path)
	if old.Kind != tc.kind {
		t.Fatalf("trash kind = %q, want %q", old.Kind, tc.kind)
	}

	// Uniqueness only applies to live rows: the path can be published again.
	tpoPut(t, env, target, newContent, http.StatusCreated)

	scoped := "/api/v1/repositories/" + repo + "/trash/" + old.Kind + "/" + old.ID + "/restore"
	global := "/api/v1/trash/" + old.Kind + "/" + old.ID + "/restore"

	tpoExpectError(t, tpoSend(t, env, tpoWriter, http.MethodPost, scoped, nil), http.StatusConflict, "trash_conflict", "repository-scoped restore")
	tpoExpectError(t, tpoSend(t, env, tpoAdmin, http.MethodPost, global, nil), http.StatusConflict, "trash_conflict", "admin restore")

	tpoAssertServes(t, env, target, newContent)

	if got := tpoRepoTrash(t, env, repo); got.Total != 1 || got.Items[0].ID != old.ID {
		t.Fatalf("trash after the refused restores = %+v, want the old copy only", got)
	}

	// Move the new copy out of the way (it goes to the trash itself) and the
	// old one comes back.
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, target, nil), http.StatusNoContent, "delete the new copy")
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodPost, scoped, nil), http.StatusOK, "restore once the path is free")
	tpoAssertServes(t, env, target, oldContent)
}

// 6. Quota ----------------------------------------------------------------------------

func TestTrashPolicies_QuotaCountsTrashedBytesUntilPurge(t *testing.T) {
	env := tpoNewEnv(t)

	const repo = "tpo-quota"

	tpoCreateRepo(t, env, repo, "maven", nil)
	// Room for one artifact.
	tpoExec(t, `INSERT INTO storage_quotas (repository_id, max_artifact_count)
		VALUES ((SELECT id FROM repositories WHERE name = $1), 1)
		ON CONFLICT (repository_id) DO UPDATE SET max_artifact_count = 1`, repo)

	first, second := tpoContent("first"), tpoContent("second")
	firstPath, secondPath := tpoMavenPath("first", "1.0.0"), tpoMavenPath("second", "1.0.0")

	tpoPut(t, env, tpoMavenURL(repo, firstPath), first, http.StatusCreated)
	tpoAssertQuota(t, repo, len(first), 1)
	tpoPut(t, env, tpoMavenURL(repo, secondPath), second, http.StatusInsufficientStorage)

	// Soft delete: the trashed bytes keep counting, the quota stays full.
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repo, firstPath), nil), http.StatusNoContent, "delete first")
	tpoAssertQuota(t, repo, len(first), 1)
	tpoPut(t, env, tpoMavenURL(repo, secondPath), second, http.StatusInsufficientStorage)

	// A manual purge gives the quota back.
	item := tpoTrashItem(t, env, repo, firstPath)
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, "/api/v1/trash/"+item.Kind+"/"+item.ID, nil), http.StatusNoContent, "purge first")
	tpoAssertQuota(t, repo, 0, 0)

	tpoPut(t, env, tpoMavenURL(repo, secondPath), second, http.StatusCreated)
	tpoAssertQuota(t, repo, len(second), 1)

	// So does the retention purge.
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repo, secondPath), nil), http.StatusNoContent, "delete second")
	tpoAssertQuota(t, repo, len(second), 1)
	tpoAge(t, "maven_artifacts", repo, 8)

	if stats := tpoRunPurge(t); stats.Purged != 1 {
		t.Fatalf("purge pass = %+v, want 1 purged", stats)
	}

	tpoAssertQuota(t, repo, 0, 0)

	// Retention 0 deletes at once and releases at once.
	tpoPatchRepo(t, env, repo, `{"trash_retention_days":0}`)
	tpoPut(t, env, tpoMavenURL(repo, firstPath), first, http.StatusCreated)
	tpoAssertQuota(t, repo, len(first), 1)
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(repo, firstPath), nil), http.StatusNoContent, "hard delete first")
	tpoAssertQuota(t, repo, 0, 0)
}

// 7. Audit --------------------------------------------------------------------------------

// tpoAuditRow is what the audit log keeps of one artifact event.
type tpoAuditRow struct {
	action    string
	actorType string
	actor     string
	message   string
}

// tpoAuditTrail lists the artifact.* audit events of repo, oldest first.
func tpoAuditTrail(t *testing.T, repo string) []tpoAuditRow {
	t.Helper()

	rows, err := harness.DB.QueryContext(t.Context(),
		`SELECT a.action, COALESCE(a.actor_type, ''), COALESCE(a.details_json->>'actor', ''), COALESCE(a.details_json->>'message', '')
		   FROM audit_logs a JOIN repositories r ON r.id = a.repository_id
		  WHERE r.name = $1 AND a.action LIKE 'artifact.%'
		  ORDER BY a.created_at, a.id`, repo)
	if err != nil {
		t.Fatalf("query audit log: %v", err)
	}

	defer rows.Close()

	var trail []tpoAuditRow

	for rows.Next() {
		var row tpoAuditRow
		if err := rows.Scan(&row.action, &row.actorType, &row.actor, &row.message); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}

		trail = append(trail, row)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit rows: %v", err)
	}

	return trail
}

func TestTrashPolicies_AuditTrail(t *testing.T) {
	env := tpoNewEnv(t)

	// The harness has no audit logger; wire the real one for this test.
	logger := audit.NewLogger(harness.Database, harness.Log, nil, 100)
	audit.SetDefault(logger)
	t.Cleanup(func() {
		audit.SetDefault(nil)
		_ = logger.Shutdown(context.Background())
	})

	const repo, hardRepo = "tpo-audit", "tpo-audit-hard"

	tpoCreateRepo(t, env, repo, "maven", nil)
	tpoCreateRepo(t, env, hardRepo, "maven", map[string]any{"trash_retention_days": 0})

	path := tpoMavenPath("app", "1.0.0")
	target := tpoMavenURL(repo, path)
	trashed := func(who string) {
		tpoExpect(t, tpoSend(t, env, who, http.MethodDelete, target, nil), http.StatusNoContent, "delete")
	}

	// delete -> restore -> delete -> manual purge -> publish, delete -> retention purge.
	tpoPut(t, env, target, tpoContent("one"), http.StatusCreated)
	trashed(tpoWriter)

	item := tpoTrashItem(t, env, repo, path)
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodPost, "/api/v1/trash/"+item.Kind+"/"+item.ID+"/restore", nil), http.StatusOK, "restore")

	trashed(tpoWriter)

	item = tpoTrashItem(t, env, repo, path)
	tpoExpect(t, tpoSend(t, env, tpoAdmin, http.MethodDelete, "/api/v1/trash/"+item.Kind+"/"+item.ID, nil), http.StatusNoContent, "manual purge")

	tpoPut(t, env, target, tpoContent("two"), http.StatusCreated)
	trashed(tpoWriter)
	tpoAge(t, "maven_artifacts", repo, 8)

	if stats := tpoRunPurge(t); stats.Purged != 1 {
		t.Fatalf("purge pass = %+v, want 1 purged", stats)
	}

	// A permanent delete is audited as such.
	tpoPut(t, env, tpoMavenURL(hardRepo, path), tpoContent("three"), http.StatusCreated)
	tpoExpect(t, tpoSend(t, env, tpoWriter, http.MethodDelete, tpoMavenURL(hardRepo, path), nil), http.StatusNoContent, "hard delete")

	// Drain the asynchronous audit writer before reading.
	if err := logger.Shutdown(t.Context()); err != nil {
		t.Fatalf("audit shutdown: %v", err)
	}

	writer := env.writer.Username
	want := []tpoAuditRow{
		{"artifact.deleted", "user", writer, "moved to trash: " + path},
		{"artifact.restored", "user", tpoAdminName, "restored: " + path + " 1.0.0"},
		{"artifact.deleted", "user", writer, "moved to trash: " + path},
		{"artifact.purged", "user", tpoAdminName, "purged (manual): " + path + " 1.0.0"},
		{"artifact.deleted", "user", writer, "moved to trash: " + path},
		{"artifact.purged", "system", "", "purged (retention): " + path + " 1.0.0"},
	}

	if got := tpoAuditTrail(t, repo); !slices.Equal(got, want) {
		t.Errorf("audit trail of %s =\n%+v\nwant\n%+v", repo, got, want)
	}

	wantHard := []tpoAuditRow{{"artifact.deleted", "user", writer, "deleted: " + path}}
	if got := tpoAuditTrail(t, hardRepo); !slices.Equal(got, wantHard) {
		t.Errorf("audit trail of %s = %+v, want %+v", hardRepo, got, wantHard)
	}
}
