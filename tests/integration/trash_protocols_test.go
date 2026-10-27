// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/worker"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

// Soft delete (trash) over the native protocols of Maven, npm and Debian.
//
// Every test drives the real publish/unpublish/delete handlers, the real
// native read handlers, the real metadata workers and the real TrashHandler
// over Postgres and the filesystem blob store, mounted the way cmd/server
// wires them. Only authentication is replaced: a middleware resolves the
// principal named by the X-Tpr-Who header, the way the RBAC middleware does
// from credentials.

const (
	// tprAdmin is a legacy-key principal: allowed everything.
	tprAdmin = "admin"
	// tprDev holds read, write and delete on the test repository only.
	tprDev = "dev"
	// tprStranger is authenticated but holds no permission at all.
	tprStranger = "stranger"

	tprWhoHeader     = "X-Tpr-Who"
	tprBaseURL       = "http://openartifact.test"
	tprRetentionDays = 7
	tprWaitTimeout   = 30 * time.Second
	tprDebounce      = 10 * time.Millisecond
	tprDebDist       = "stable"
	tprDebComp       = "main"
	tprDebArch       = "amd64"
	tprMavenGroup    = "com.acme"
)

// tprEnv is one repository served by the full handler stack.
type tprEnv struct {
	t          *testing.T
	router     http.Handler
	principals map[string]*auth.Principal
	repo       *models.Repository
	devName    string
}

// tprUser creates a user holding the given actions on repositories matching
// repoFilter (no permission at all when repoFilter is empty).
func tprUser(t *testing.T, username, repoFilter string, actions []string) *auth.Principal {
	t.Helper()

	ctx := t.Context()

	user, err := harness.Database.CreateUser(ctx, &models.User{Username: username})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}

	if repoFilter != "" {
		target, err := harness.Database.CreatePermissionTarget(ctx, &models.PermissionTarget{
			Name: username + "-target", RepositoryFilter: repoFilter, PathPattern: ".*", Actions: actions,
		})
		if err != nil {
			t.Fatalf("CreatePermissionTarget(%s): %v", username, err)
		}

		if err := harness.Database.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
			PermissionTargetID: target.ID, PrincipalType: "user", PrincipalID: user.ID,
		}); err != nil {
			t.Fatalf("CreatePermissionAssignment(%s): %v", username, err)
		}
	}

	return &auth.Principal{UserID: &user.ID, Username: username}
}

// tprNewEnv resets the database, creates a hosted repository of the given
// format with a 7-day trash retention and mounts every handler the soft
// delete scenarios need.
func tprNewEnv(t *testing.T, name, format string) *tprEnv {
	t.Helper()

	harness.Reset(t)

	retention := tprRetentionDays
	orgID := systemOrgID
	repo := &models.Repository{
		Name: name, Format: format, RepoType: "hosted", ProjectID: &orgID, TrashRetentionDays: &retention,
	}

	if err := harness.Database.CreateRepository(t.Context(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", name, err)
	}

	// Users outlive Reset: keep their names unique across runs.
	suffix := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
	devName := "tpr-dev-" + suffix

	e := &tprEnv{
		t:       t,
		repo:    repo,
		devName: devName,
		principals: map[string]*auth.Principal{
			tprAdmin:    {Username: "tpr-admin", IsLegacy: true, Credential: auth.CredentialLegacy},
			tprDev:      tprUser(t, devName, "^"+name+"$", []string{"read", "write", "delete"}),
			tprStranger: tprUser(t, "tpr-stranger-"+suffix, "", nil),
		},
	}

	e.router = e.buildRouter()

	return e
}

// authenticate resolves the principal named by the X-Tpr-Who header (anonymous
// when absent).
func (e *tprEnv) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := e.principals[r.Header.Get(tprWhoHeader)]
		if !ok {
			p = &auth.Principal{Username: "anonymous", IsAnonymous: true, Credential: auth.CredentialNone}
		}

		next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), p)))
	})
}

// buildRouter mounts the handlers like cmd/server/main.go: the npm, deb and
// maven read/write handlers, the metadata workers they notify, the repository
// listing, search and the trash API with its regenerator.
func (e *tprEnv) buildRouter() http.Handler {
	t := e.t
	t.Helper()

	db, bs, log := harness.Database, harness.BlobStore, harness.Log

	mavenW := worker.NewMavenMetadataWorker(db, bs, harness.DB, log, nil).WithDebounce(tprDebounce)
	npmW := worker.NewNPMMetadataWorker(db, log, harness.DB, nil).WithDebounce(tprDebounce)
	debW := worker.NewDebMetadataWorker(db, bs, nil, log).WithDebounce(tprDebounce)

	// Stop drains pending regenerations, so none outlives the test's data.
	t.Cleanup(func() {
		mavenW.Stop()
		npmW.Stop()
		debW.Stop()
	})

	evaluator := auth.NewEvaluator(db, log, false)

	mavenUpload := api.NewMavenUploadHandler(log, db, bs, nil)
	mavenUpload.SetMavenWorker(mavenW)

	npmUpload := api.NewNPMUploadHandler(log, db, bs, tprBaseURL, nil)
	npmUpload.SetNPMWorker(npmW)

	debUpload := api.NewDebUploadHandler(log, db, bs, nil)
	debUpload.SetDebWorker(debW)

	mavenRead := formatmaven.NewHTTPHandler(log, db, bs)
	npmRead := formatnpm.NewHTTPHandler(log, db, bs, tprBaseURL)
	debRead := formatdeb.NewHTTPHandler(log, db, bs)

	trashHandler := api.NewTrashHandler(db, log, evaluator).
		WithBlobStore(bs).
		WithRegenerator(api.NewWorkerRegenerator(debW, nil, nil, nil, mavenW, npmW))

	r := chi.NewRouter()
	r.Use(e.authenticate)

	r.Route("/repos/{name}", func(r chi.Router) {
		debRead.RegisterRepoRoutes(r)
		npmRead.RegisterRepoRoutes(r)
		npmUpload.RegisterRepoRoutes(r)
	})
	r.Route("/repos/maven/{name}", mavenRead.RegisterRepoRoutes)
	mavenUpload.Register(r)
	debUpload.Register(r)

	api.NewRepositoriesHandler(db, log, evaluator).RegisterAuthed(r)
	trashHandler.RegisterAuthed(r)
	r.Get("/api/v1/search", search.NewHandler(harness.DB, evaluator, log).ServeHTTP)

	return r
}

// do serves one request as the named principal ("" is anonymous). headers is a
// flat list of name, value pairs.
func (e *tprEnv) do(method, target, who string, body []byte, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req := httptest.NewRequestWithContext(e.t.Context(), method, target, reader)
	if who != "" {
		req.Header.Set(tprWhoHeader, who)
	}

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func (e *tprEnv) get(target, who string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(http.MethodGet, target, who, nil, headers...)
}

// expect fails the test unless rec carries the wanted status.
func (e *tprEnv) expect(rec *httptest.ResponseRecorder, want int) {
	e.t.Helper()

	if rec.Code != want {
		body := rec.Body.String()
		if len(body) > 300 {
			body = body[:300]
		}

		e.t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, body)
	}
}

// tprDecode decodes a JSON response body.
func tprDecode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}

	return v
}

// tprWaitUntil polls check until it holds. check reports what it observed so a
// timeout says why the condition never became true.
func tprWaitUntil(t *testing.T, what string, check func() (bool, string)) {
	t.Helper()

	timeout := time.NewTimer(tprWaitTimeout)
	defer timeout.Stop()

	tick := time.NewTicker(tprDebounce)
	defer tick.Stop()

	for {
		ok, observed := check()
		if ok {
			return
		}

		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatalf("timed out after %s waiting for %s; last observed: %s", tprWaitTimeout, what, observed)
		case <-t.Context().Done():
			t.Fatalf("test ended while waiting for %s; last observed: %s", what, observed)
		}
	}
}

// --- listings, search and trash API -------------------------------------

// tprListedVersions returns the version of every row the repository's package
// listing (the UI's components view) reports.
func (e *tprEnv) listedVersions() []string {
	e.t.Helper()

	rec := e.get("/api/v1/repositories/"+e.repo.Name+"/packages?limit=100", tprAdmin)
	e.expect(rec, http.StatusOK)

	resp := tprDecode[struct {
		Packages []struct {
			Version string `json:"version"`
		} `json:"packages"`
	}](e.t, rec)

	versions := make([]string, 0, len(resp.Packages))
	for _, p := range resp.Packages {
		versions = append(versions, p.Version)
	}

	return versions
}

// searchedVersions returns the version of every search hit in the repository.
func (e *tprEnv) searchedVersions() []string {
	e.t.Helper()

	rec := e.get("/api/v1/search?limit=100&q="+url.QueryEscape("repo:"+e.repo.Name), tprAdmin)
	e.expect(rec, http.StatusOK)

	resp := tprDecode[struct {
		Packages []search.SearchResult `json:"packages"`
	}](e.t, rec)

	versions := make([]string, 0, len(resp.Packages))
	for _, p := range resp.Packages {
		versions = append(versions, p.Version)
	}

	return versions
}

func tprCount(versions []string, version string) int {
	n := 0

	for _, v := range versions {
		if v == version {
			n++
		}
	}

	return n
}

// visibility is how many rows the listing and the search show for a version.
type tprVisibility struct{ listed, searched int }

func (e *tprEnv) visibility(version string) tprVisibility {
	e.t.Helper()

	return tprVisibility{
		listed:   tprCount(e.listedVersions(), version),
		searched: tprCount(e.searchedVersions(), version),
	}
}

// assertVisible fails unless the version shows up in both listing and search,
// and returns the counts.
func (e *tprEnv) assertVisible(version string) tprVisibility {
	e.t.Helper()

	got := e.visibility(version)
	if got.listed == 0 || got.searched == 0 {
		e.t.Fatalf("version %s: listing rows = %d, search hits = %d; want both > 0", version, got.listed, got.searched)
	}

	return got
}

func (e *tprEnv) assertHidden(version string) {
	e.t.Helper()

	if got := e.visibility(version); got != (tprVisibility{}) {
		e.t.Fatalf("version %s still shown: listing rows = %d, search hits = %d", version, got.listed, got.searched)
	}
}

// trashItems lists the repository's trash through the admin API.
func (e *tprEnv) trashItems() []responses.TrashItemResponse {
	e.t.Helper()

	rec := e.get("/api/v1/trash?limit=100&repository="+url.QueryEscape(e.repo.Name), tprAdmin)
	e.expect(rec, http.StatusOK)

	return tprDecode[responses.TrashListResponse](e.t, rec).Items
}

// assertTrashed checks the trash holds exactly the wanted names (for the kind),
// each deleted by the dev principal with the 7-day retention, and returns the
// items keyed by name.
func (e *tprEnv) assertTrashed(kind models.TrashKind, version string, wantNames ...string) map[string]responses.TrashItemResponse {
	e.t.Helper()

	items := e.trashItems()
	byName := make(map[string]responses.TrashItemResponse, len(items))
	got := make([]string, 0, len(items))

	for _, it := range items {
		got = append(got, it.Name)
		byName[it.Name] = it
	}

	slices.Sort(got)

	want := slices.Clone(wantNames)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		e.t.Fatalf("trash holds %v, want %v", got, want)
	}

	for _, it := range items {
		e.assertTrashItem(it, kind, version)
	}

	return byName
}

func (e *tprEnv) assertTrashItem(it responses.TrashItemResponse, kind models.TrashKind, version string) {
	e.t.Helper()

	if it.Kind != string(kind) || it.Repository != e.repo.Name || it.Version != version {
		e.t.Fatalf("trash item %+v: want kind %s, repository %s, version %s", it, kind, e.repo.Name, version)
	}

	if it.DeletedBy != e.devName {
		e.t.Errorf("trash item %s deleted_by = %q, want the deleting principal %q", it.Name, it.DeletedBy, e.devName)
	}

	deletedAt, err := time.Parse(time.RFC3339, it.DeletedAt)
	if err != nil {
		e.t.Fatalf("deleted_at %q: %v", it.DeletedAt, err)
	}

	purgeAt, err := time.Parse(time.RFC3339, it.PurgeAt)
	if err != nil {
		e.t.Fatalf("purge_at %q: %v", it.PurgeAt, err)
	}

	if retention := purgeAt.Sub(deletedAt); retention < tprRetentionDays*24*time.Hour-time.Minute ||
		retention > tprRetentionDays*24*time.Hour+time.Minute {
		e.t.Errorf("trash item %s retention = %s, want %d days", it.Name, retention, tprRetentionDays)
	}
}

// assertTrashAccess checks the trash API is closed to principals that may not
// manage it: the global routes are admin-only and the repository routes need
// delete on the repository.
func (e *tprEnv) assertTrashAccess(kind models.TrashKind, id string) {
	e.t.Helper()

	repoTrash := "/api/v1/repositories/" + e.repo.Name + "/trash"
	global := "/api/v1/trash/" + string(kind) + "/" + id + "/restore"
	scoped := repoTrash + "/" + string(kind) + "/" + id + "/restore"

	for _, tc := range []struct {
		method, target, who string
	}{
		{http.MethodGet, "/api/v1/trash", tprDev},
		{http.MethodGet, "/api/v1/trash", tprStranger},
		{http.MethodPost, global, tprDev},
		{http.MethodPost, global, tprStranger},
		{http.MethodGet, repoTrash, tprStranger},
		{http.MethodPost, scoped, tprStranger},
	} {
		if rec := e.do(tc.method, tc.target, tc.who, nil); rec.Code != http.StatusForbidden {
			e.t.Errorf("%s %s as %s = %d, want 403", tc.method, tc.target, tc.who, rec.Code)
		}
	}

	if rec := e.do(http.MethodGet, repoTrash, tprDev, nil); rec.Code != http.StatusOK {
		e.t.Errorf("GET %s as dev = %d, want 200", repoTrash, rec.Code)
	}
}

// restoreGlobal restores through the admin route.
func (e *tprEnv) restoreGlobal(kind models.TrashKind, id string) responses.TrashItemResponse {
	e.t.Helper()

	rec := e.do(http.MethodPost, "/api/v1/trash/"+string(kind)+"/"+id+"/restore", tprAdmin, nil)
	e.expect(rec, http.StatusOK)

	return tprDecode[responses.TrashItemResponse](e.t, rec)
}

// restoreScoped restores through the repository route as the dev principal.
func (e *tprEnv) restoreScoped(kind models.TrashKind, id string) responses.TrashItemResponse {
	e.t.Helper()

	rec := e.do(http.MethodPost, "/api/v1/repositories/"+e.repo.Name+"/trash/"+string(kind)+"/"+id+"/restore", tprDev, nil)
	e.expect(rec, http.StatusOK)

	return tprDecode[responses.TrashItemResponse](e.t, rec)
}

// assertRestoredOnce checks a restore answer and that the item left the trash:
// a second restore finds nothing.
func (e *tprEnv) assertRestoredOnce(it responses.TrashItemResponse, kind models.TrashKind, wantID string) {
	e.t.Helper()

	if it.ID != wantID || it.Kind != string(kind) {
		e.t.Fatalf("restore answered %+v, want id %s kind %s", it, wantID, kind)
	}

	rec := e.do(http.MethodPost, "/api/v1/trash/"+string(kind)+"/"+wantID+"/restore", tprAdmin, nil)
	e.expect(rec, http.StatusNotFound)
}

// --- Maven ---------------------------------------------------------------

// tprFile is one published Maven file.
type tprFile struct {
	path string
	data []byte
	id   string
}

func tprJar(t *testing.T, marker string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}

	if _, err := w.Write([]byte("Manifest-Version: 1.0\nImplementation-Title: " + marker + "\n")); err != nil {
		t.Fatalf("zip write: %v", err)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	return buf.Bytes()
}

func tprPOM(artifact, version string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>` + tprMavenGroup + `</groupId>
  <artifactId>` + artifact + `</artifactId>
  <version>` + version + `</version>
</project>
`)
}

func (e *tprEnv) mavenURL(p string) string {
	return "/repos/maven/" + e.repo.Name + "/" + p
}

// putMaven publishes one file through the real PUT handler as the dev principal.
func (e *tprEnv) putMaven(artifact, version, ext string, data []byte) tprFile {
	e.t.Helper()

	p := fmt.Sprintf("%s/%s/%s/%s-%s.%s", strings.ReplaceAll(tprMavenGroup, ".", "/"), artifact, version, artifact, version, ext)

	rec := e.do(http.MethodPut, e.mavenURL(p), tprDev, data)
	e.expect(rec, http.StatusCreated)

	id := rec.Header().Get("X-Oa-Artifact-Id")
	if id == "" {
		e.t.Fatalf("PUT %s answered no X-Oa-Artifact-Id", p)
	}

	return tprFile{path: p, data: data, id: id}
}

// publishMaven publishes the jar and the pom of one version.
func (e *tprEnv) publishMaven(artifact, version string) (tprFile, tprFile) {
	e.t.Helper()

	jar := e.putMaven(artifact, version, "jar", tprJar(e.t, artifact+"@"+version))
	pom := e.putMaven(artifact, version, "pom", tprPOM(artifact, version))

	return jar, pom
}

// tprMavenDigests returns the hex digests Maven clients verify, keyed by the
// sidecar suffix.
func tprMavenDigests(data []byte) map[string]string {
	md5sum := md5.Sum(data)
	sha1sum := sha1.Sum(data)
	sha256sum := sha256.Sum256(data)
	sha512sum := sha512.Sum512(data)

	return map[string]string{
		".md5":    hex.EncodeToString(md5sum[:]),
		".sha1":   hex.EncodeToString(sha1sum[:]),
		".sha256": hex.EncodeToString(sha256sum[:]),
		".sha512": hex.EncodeToString(sha512sum[:]),
	}
}

// assertMavenServed checks a file downloads byte-identical with correct
// checksum sidecars, and returns the sidecar bodies.
func (e *tprEnv) assertMavenServed(f tprFile) map[string]string {
	e.t.Helper()

	rec := e.get(e.mavenURL(f.path), "")
	e.expect(rec, http.StatusOK)

	if !bytes.Equal(rec.Body.Bytes(), f.data) {
		e.t.Fatalf("GET %s: body differs from the published bytes (%d vs %d bytes)", f.path, rec.Body.Len(), len(f.data))
	}

	sums := make(map[string]string)

	for suffix, digest := range tprMavenDigests(f.data) {
		side := e.get(e.mavenURL(f.path+suffix), "")
		e.expect(side, http.StatusOK)

		if want := digest + "  " + path.Base(f.path) + "\n"; side.Body.String() != want {
			e.t.Fatalf("GET %s%s = %q, want %q", f.path, suffix, side.Body.String(), want)
		}

		sums[suffix] = side.Body.String()
	}

	return sums
}

// assertMavenGone checks the file and every checksum sidecar answer 404.
func (e *tprEnv) assertMavenGone(f tprFile) {
	e.t.Helper()

	for _, suffix := range []string{"", ".md5", ".sha1", ".sha256", ".sha512"} {
		if rec := e.get(e.mavenURL(f.path+suffix), ""); rec.Code != http.StatusNotFound {
			e.t.Errorf("GET %s%s = %d, want 404 after delete", f.path, suffix, rec.Code)
		}
	}
}

// deleteMaven removes one file through the real DELETE handler.
func (e *tprEnv) deleteMaven(f tprFile) {
	e.t.Helper()

	e.expect(e.do(http.MethodDelete, e.mavenURL(f.path), tprDev, nil), http.StatusNoContent)
}

// tprMavenMeta is the part of maven-metadata.xml that lists versions.
type tprMavenMeta struct {
	Latest   string   `xml:"versioning>latest"`
	Release  string   `xml:"versioning>release"`
	Versions []string `xml:"versioning>versions>version"`
}

// mavenMeta fetches and parses the artifact's maven-metadata.xml.
func (e *tprEnv) mavenMeta(artifact string) (int, tprMavenMeta, string) {
	e.t.Helper()

	target := e.mavenURL(strings.ReplaceAll(tprMavenGroup, ".", "/") + "/" + artifact + "/maven-metadata.xml")
	rec := e.get(target, "")

	var meta tprMavenMeta
	if rec.Code == http.StatusOK {
		if err := xml.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
			e.t.Fatalf("parse maven-metadata.xml %q: %v", rec.Body.String(), err)
		}
	}

	return rec.Code, meta, rec.Body.String()
}

// waitMavenVersions waits until the generated metadata lists exactly want
// (ascending) with the newest as latest and release.
func (e *tprEnv) waitMavenVersions(artifact string, want ...string) {
	e.t.Helper()

	newest := want[len(want)-1]

	tprWaitUntil(e.t, "maven-metadata.xml of "+artifact+" to list "+strings.Join(want, ", "), func() (bool, string) {
		status, meta, raw := e.mavenMeta(artifact)
		if status != http.StatusOK {
			return false, fmt.Sprintf("status %d", status)
		}

		return slices.Equal(meta.Versions, want) && meta.Latest == newest && meta.Release == newest, raw
	})
}

// waitMavenNoVersions waits until the metadata stops advertising any version:
// either it is gone (404) or it lists none.
func (e *tprEnv) waitMavenNoVersions(artifact string) {
	e.t.Helper()

	tprWaitUntil(e.t, "maven-metadata.xml of "+artifact+" to list no version", func() (bool, string) {
		status, meta, raw := e.mavenMeta(artifact)
		if status == http.StatusNotFound {
			return true, ""
		}

		return status == http.StatusOK && len(meta.Versions) == 0, raw
	})
}

func TestTrashProtocols_Maven(t *testing.T) {
	e := tprNewEnv(t, "tpr-maven", "maven")

	keepJar, keepPOM := e.publishMaven("app", "1.0.0")
	jar, pom := e.publishMaven("app", "2.0.0")

	e.waitMavenVersions("app", "1.0.0", "2.0.0")

	jarSums := e.assertMavenServed(jar)
	pomSums := e.assertMavenServed(pom)
	e.assertMavenServed(keepJar)
	e.assertMavenServed(keepPOM)

	visibleBefore := e.assertVisible("2.0.0")
	e.assertVisible("1.0.0")

	// Deleting the jar hides exactly the jar; its pom keeps serving until it
	// is deleted too.
	e.deleteMaven(jar)
	e.assertMavenGone(jar)
	e.assertMavenServed(pom)

	e.deleteMaven(pom)
	e.assertMavenGone(pom)

	// The other version is untouched and the generated metadata drops 2.0.0.
	e.assertMavenServed(keepJar)
	e.assertMavenServed(keepPOM)
	e.waitMavenVersions("app", "1.0.0")
	e.assertHidden("2.0.0")
	e.assertVisible("1.0.0")

	// The files are in the trash, attributed to the deleting principal; the
	// bytes stay in the blob store until the retention purge.
	trashed := e.assertTrashed(models.TrashKindMaven, "2.0.0", jar.path, pom.path)

	for _, f := range []tprFile{jar, pom} {
		it := trashed[f.path]
		if it.ID != f.id {
			t.Errorf("trash item %s has id %s, want the artifact id %s", f.path, it.ID, f.id)
		}

		sum := sha256.Sum256(f.data)
		if _, exists, err := harness.BlobStore.ExistsBySHA256(t.Context(), hex.EncodeToString(sum[:])); err != nil || !exists {
			t.Errorf("blob of %s: exists = %v, err = %v; want kept until purge", f.path, exists, err)
		}
	}

	e.assertTrashAccess(models.TrashKindMaven, jar.id)

	// Restore the jar through the repository route (as the dev principal) and
	// the pom through the admin route.
	e.assertRestoredOnce(e.restoreScoped(models.TrashKindMaven, jar.id), models.TrashKindMaven, jar.id)
	e.assertRestoredOnce(e.restoreGlobal(models.TrashKindMaven, pom.id), models.TrashKindMaven, pom.id)

	if left := e.trashItems(); len(left) != 0 {
		t.Fatalf("trash still holds %+v after restoring everything", left)
	}

	// Identical bytes and checksums, and the metadata lists the version again.
	if got := e.assertMavenServed(jar); !tprSameStrings(got, jarSums) {
		t.Errorf("jar checksums after restore = %v, want %v", got, jarSums)
	}

	if got := e.assertMavenServed(pom); !tprSameStrings(got, pomSums) {
		t.Errorf("pom checksums after restore = %v, want %v", got, pomSums)
	}

	e.waitMavenVersions("app", "1.0.0", "2.0.0")

	if got := e.assertVisible("2.0.0"); got != visibleBefore {
		t.Errorf("visibility after restore = %+v, want %+v", got, visibleBefore)
	}
}

// The last version of an artifact is a boundary of its own: once nothing
// remains, maven-metadata.xml must stop advertising the version.
func TestTrashProtocols_MavenLastVersion(t *testing.T) {
	e := tprNewEnv(t, "tpr-maven-last", "maven")

	jar, pom := e.publishMaven("solo", "1.0.0")
	e.waitMavenVersions("solo", "1.0.0")

	e.deleteMaven(jar)
	e.deleteMaven(pom)
	e.assertMavenGone(jar)
	e.assertMavenGone(pom)
	e.waitMavenNoVersions("solo")
	e.assertHidden("1.0.0")

	e.restoreGlobal(models.TrashKindMaven, jar.id)
	e.restoreGlobal(models.TrashKindMaven, pom.id)

	e.assertMavenServed(jar)
	e.assertMavenServed(pom)
	e.waitMavenVersions("solo", "1.0.0")
	e.assertVisible("1.0.0")
}

func tprSameStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

// --- npm -----------------------------------------------------------------

// tprNPMVersion is one version of an npm package prepared for publishing.
type tprNPMVersion struct {
	name      string
	version   string
	tarball   []byte
	manifest  json.RawMessage
	integrity string
	shasum    string
}

func tprNPMTarball(t *testing.T, name, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	files := map[string]string{
		"package/package.json": fmt.Sprintf(`{"name":%q,"version":%q}`, name, version),
		"package/index.js":     fmt.Sprintf("module.exports = %q;\n", name+"@"+version),
	}

	for _, fname := range []string{"package/package.json", "package/index.js"} {
		content := files[fname]
		if err := tw.WriteHeader(&tar.Header{Name: fname, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("tar header: %v", err)
		}

		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	return buf.Bytes()
}

func tprNPMRelease(t *testing.T, name, version string) tprNPMVersion {
	t.Helper()

	tgz := tprNPMTarball(t, name, version)

	integrity, shasum, _, _, err := pkgnpm.ComputeIntegrity(bytes.NewReader(tgz))
	if err != nil {
		t.Fatalf("ComputeIntegrity: %v", err)
	}

	manifest, err := json.Marshal(map[string]any{
		"name":        name,
		"version":     version,
		"description": "soft delete protocol test",
		"license":     "MIT",
		"dist": map[string]string{
			"integrity": integrity,
			"shasum":    shasum,
			"tarball":   tprBaseURL + "/placeholder.tgz",
		},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	return tprNPMVersion{name: name, version: version, tarball: tgz, manifest: manifest, integrity: integrity, shasum: shasum}
}

func (v tprNPMVersion) tarballName() string {
	return v.name + "-" + v.version + ".tgz"
}

func (e *tprEnv) npmURL(parts ...string) string {
	return "/repos/" + e.repo.Name + "/" + strings.Join(parts, "/")
}

func (e *tprEnv) tarballURL(v tprNPMVersion) string {
	return e.npmURL(v.name, "-", v.tarballName())
}

// publishNPM publishes one version through the real publish handler.
func (e *tprEnv) publishNPM(v tprNPMVersion) {
	e.t.Helper()

	body, err := json.Marshal(map[string]any{
		"_id":       v.name,
		"name":      v.name,
		"versions":  map[string]json.RawMessage{v.version: v.manifest},
		"dist-tags": map[string]string{"latest": v.version},
		"_attachments": map[string]any{
			v.tarballName(): map[string]any{
				"content_type": "application/octet-stream",
				"data":         base64.StdEncoding.EncodeToString(v.tarball),
				"length":       len(v.tarball),
			},
		},
	})
	if err != nil {
		e.t.Fatalf("marshal publish body: %v", err)
	}

	e.expect(e.do(http.MethodPut, e.npmURL(v.name), tprDev, body), http.StatusCreated)
}

// unpublishNPMVersion runs `npm unpublish pkg@version`: a PUT of the packument
// without the version, then a DELETE of its tarball.
func (e *tprEnv) unpublishNPMVersion(gone tprNPMVersion, remaining ...tprNPMVersion) {
	e.t.Helper()

	versions := make(map[string]json.RawMessage, len(remaining))
	for _, v := range remaining {
		versions[v.version] = v.manifest
	}

	body, err := json.Marshal(map[string]any{
		"_id":       gone.name,
		"name":      gone.name,
		"versions":  versions,
		"dist-tags": map[string]string{"latest": remaining[len(remaining)-1].version},
	})
	if err != nil {
		e.t.Fatalf("marshal packument: %v", err)
	}

	e.expect(e.do(http.MethodPut, e.npmURL(gone.name, "-rev", "1-tpr"), tprDev, body), http.StatusOK)
	e.expect(e.do(http.MethodDelete, e.npmURL(gone.name, "-", gone.tarballName(), "-rev", "2-tpr"), tprDev, nil), http.StatusOK)
}

// tprPackument is a decoded packument.
type tprPackument struct {
	versions map[string]json.RawMessage
	tags     map[string]string
}

// packument fetches the packument; the returned value is empty unless the
// answer is 200.
func (e *tprEnv) packument(name string, headers ...string) (*httptest.ResponseRecorder, tprPackument) {
	e.t.Helper()

	rec := e.get(e.npmURL(name), "", headers...)

	var p tprPackument
	if rec.Code != http.StatusOK {
		return rec, p
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		e.t.Fatalf("decode packument: %v", err)
	}

	if err := json.Unmarshal(top["versions"], &p.versions); err != nil {
		e.t.Fatalf("decode packument versions: %v", err)
	}

	if err := json.Unmarshal(top["dist-tags"], &p.tags); err != nil {
		e.t.Fatalf("decode packument dist-tags: %v", err)
	}

	return rec, p
}

// tprDist is the dist object of a version manifest.
type tprDist struct {
	Dist struct {
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
		Tarball   string `json:"tarball"`
	} `json:"dist"`
}

// assertNPMServed checks a version is installable: listed in the full and the
// abbreviated packument with the integrity and shasum of the published tarball,
// resolvable on its own and downloadable byte-identical. It returns the
// version's manifest exactly as the packument serves it.
func (e *tprEnv) assertNPMServed(v tprNPMVersion) json.RawMessage {
	e.t.Helper()

	var served json.RawMessage

	for _, accept := range []string{"", "application/vnd.npm.install-v1+json"} {
		_, p := e.packument(v.name, "Accept", accept)

		manifest, ok := p.versions[v.version]
		if !ok {
			e.t.Fatalf("packument (Accept %q) lacks %s@%s; has %d versions", accept, v.name, v.version, len(p.versions))
		}

		var dist tprDist
		if err := json.Unmarshal(manifest, &dist); err != nil {
			e.t.Fatalf("decode manifest: %v", err)
		}

		if dist.Dist.Integrity != v.integrity || dist.Dist.Shasum != v.shasum {
			e.t.Errorf(
				"packument (Accept %q) dist of %s = %+v, want integrity %s shasum %s",
				accept,
				v.version,
				dist.Dist,
				v.integrity,
				v.shasum,
			)
		}

		if accept == "" {
			served = manifest
		}
	}

	rec := e.get(e.npmURL(v.name, v.version), "")
	e.expect(rec, http.StatusOK)

	rec = e.get(e.tarballURL(v), "")
	e.expect(rec, http.StatusOK)

	if !bytes.Equal(rec.Body.Bytes(), v.tarball) {
		e.t.Fatalf("tarball %s differs from the published bytes (%d vs %d bytes)", v.tarballName(), rec.Body.Len(), len(v.tarball))
	}

	return served
}

// assertNPMGone checks a version is invisible to clients: absent from both
// packuments (or the package is 404), its manifest and its tarball are 404.
func (e *tprEnv) assertNPMGone(v tprNPMVersion) {
	e.t.Helper()

	for _, accept := range []string{"", "application/vnd.npm.install-v1+json"} {
		rec, p := e.packument(v.name, "Accept", accept)
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
			e.t.Fatalf("packument (Accept %q) = %d, want 200 or 404", accept, rec.Code)
		}

		if _, ok := p.versions[v.version]; ok {
			e.t.Errorf("packument (Accept %q) still lists %s@%s", accept, v.name, v.version)
		}

		for tag, target := range p.tags {
			if target == v.version {
				e.t.Errorf("packument (Accept %q) still has dist-tag %s -> %s", accept, tag, target)
			}
		}
	}

	if rec := e.get(e.npmURL(v.name, v.version), ""); rec.Code != http.StatusNotFound {
		e.t.Errorf("GET version manifest %s = %d, want 404", v.version, rec.Code)
	}

	if rec := e.get(e.tarballURL(v), ""); rec.Code != http.StatusNotFound {
		e.t.Errorf("GET tarball %s = %d, want 404", v.tarballName(), rec.Code)
	}
}

func TestTrashProtocols_NPMUnpublishVersion(t *testing.T) {
	e := tprNewEnv(t, "tpr-npm", "npm")

	v1 := tprNPMRelease(t, "tpr-lib", "1.0.0")
	v2 := tprNPMRelease(t, "tpr-lib", "2.0.0")

	e.publishNPM(v1)
	e.publishNPM(v2)

	e.assertNPMServed(v1)
	servedBefore := e.assertNPMServed(v2)
	visibleBefore := e.assertVisible("2.0.0")

	rec, _ := e.packument("tpr-lib")
	etagBefore := rec.Header().Get("ETag")

	if etagBefore == "" {
		t.Fatal("packument carries no ETag")
	}

	e.unpublishNPMVersion(v2, v1)

	e.assertNPMGone(v2)
	e.assertNPMServed(v1)
	e.assertHidden("2.0.0")
	e.assertVisible("1.0.0")

	// A client holding the packument it fetched before the unpublish must
	// not be told it is still current.
	rec, _ = e.packument("tpr-lib")
	etagGone := rec.Header().Get("ETag")

	if etagGone == etagBefore {
		t.Errorf("packument ETag %s did not change after the unpublish", etagGone)
	}

	if rec := e.get(e.npmURL("tpr-lib"), "", "If-None-Match", etagBefore); rec.Code != http.StatusOK {
		t.Errorf("conditional GET with the pre-unpublish ETag = %d, want 200", rec.Code)
	}

	trashed := e.assertTrashed(models.TrashKindNPM, "2.0.0", "tpr-lib")
	e.assertTrashAccess(models.TrashKindNPM, trashed["tpr-lib"].ID)

	restored := e.restoreScoped(models.TrashKindNPM, trashed["tpr-lib"].ID)
	e.assertRestoredOnce(restored, models.TrashKindNPM, trashed["tpr-lib"].ID)

	if left := e.trashItems(); len(left) != 0 {
		t.Fatalf("trash still holds %+v after the restore", left)
	}

	// Same integrity, same shasum, same manifest, same tarball bytes.
	if servedAfter := e.assertNPMServed(v2); !bytes.Equal(servedAfter, servedBefore) {
		t.Errorf("manifest after restore = %s, want %s", servedAfter, servedBefore)
	}

	e.assertNPMServed(v1)

	if got := e.assertVisible("2.0.0"); got != visibleBefore {
		t.Errorf("visibility after restore = %+v, want %+v", got, visibleBefore)
	}

	rec, _ = e.packument("tpr-lib")
	if rec.Header().Get("ETag") == etagGone {
		t.Errorf("packument ETag %s did not change after the restore", etagGone)
	}

	if rec := e.get(e.npmURL("tpr-lib"), "", "If-None-Match", etagGone); rec.Code != http.StatusOK {
		t.Errorf("conditional GET with the pre-restore ETag = %d, want 200", rec.Code)
	}
}

// Deleting only the tarball keeps the dist-tags: the tag that pointed at the
// trashed version is hidden while it is in the trash and is back after the
// restore.
func TestTrashProtocols_NPMDistTagsSurvive(t *testing.T) {
	e := tprNewEnv(t, "tpr-npm-tags", "npm")

	v1 := tprNPMRelease(t, "tpr-tags", "1.0.0")
	v2 := tprNPMRelease(t, "tpr-tags", "2.0.0")

	e.publishNPM(v1)
	e.publishNPM(v2)

	if _, p := e.packument("tpr-tags"); p.tags["latest"] != "2.0.0" {
		t.Fatalf("latest = %q before the delete, want 2.0.0", p.tags["latest"])
	}

	e.expect(e.do(http.MethodDelete, e.npmURL("tpr-tags", "-", v2.tarballName(), "-rev", "1-tpr"), tprDev, nil), http.StatusOK)

	e.assertNPMGone(v2)
	e.assertNPMServed(v1)

	trashed := e.assertTrashed(models.TrashKindNPM, "2.0.0", "tpr-tags")
	e.restoreGlobal(models.TrashKindNPM, trashed["tpr-tags"].ID)

	e.assertNPMServed(v2)
	e.assertNPMServed(v1)

	if _, p := e.packument("tpr-tags"); p.tags["latest"] != "2.0.0" {
		t.Errorf("latest = %q after the restore, want 2.0.0", p.tags["latest"])
	}

	if rec := e.get(e.npmURL("tpr-tags", "latest"), ""); rec.Code != http.StatusOK {
		t.Errorf("GET latest = %d after the restore, want 200", rec.Code)
	}
}

// Unpublishing every version of a package leaves nothing to serve; restoring
// brings the whole package back.
func TestTrashProtocols_NPMUnpublishPackage(t *testing.T) {
	e := tprNewEnv(t, "tpr-npm-pkg", "npm")

	v1 := tprNPMRelease(t, "tpr-solo", "1.0.0")
	v2 := tprNPMRelease(t, "tpr-solo", "1.1.0")

	e.publishNPM(v1)
	e.publishNPM(v2)
	e.assertNPMServed(v1)
	e.assertNPMServed(v2)

	e.expect(e.do(http.MethodDelete, e.npmURL("tpr-solo", "-rev", "1-tpr"), tprDev, nil), http.StatusOK)

	if rec, _ := e.packument("tpr-solo"); rec.Code != http.StatusNotFound {
		t.Errorf("packument of an unpublished package = %d, want 404", rec.Code)
	}

	e.assertNPMGone(v1)
	e.assertNPMGone(v2)
	e.assertHidden("1.0.0")
	e.assertHidden("1.1.0")

	items := e.trashItems()
	if len(items) != 2 {
		t.Fatalf("trash holds %+v, want both versions", items)
	}

	for _, it := range items {
		e.assertTrashItem(it, models.TrashKindNPM, it.Version)
		e.restoreGlobal(models.TrashKindNPM, it.ID)
	}

	e.assertNPMServed(v1)
	e.assertNPMServed(v2)
	e.assertVisible("1.0.0")
	e.assertVisible("1.1.0")
}

// --- Debian --------------------------------------------------------------

// tprDeb is one uploaded Debian package.
type tprDeb struct {
	name     string
	version  string
	filename string
	data     []byte
	id       string
}

// tprBuildDeb builds a minimal .deb: an ar archive holding the control member.
func tprBuildDeb(t *testing.T, name, version string) []byte {
	t.Helper()

	ctrl := fmt.Sprintf(
		"Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: A <a@b.c>\nInstalled-Size: 10\nDescription: soft delete test of %s\n",
		name,
		version,
		tprDebArch,
		name,
	)

	var tarBuf bytes.Buffer

	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(ctrl))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}

	if _, err := tw.Write([]byte(ctrl)); err != nil {
		t.Fatalf("tar write: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	var gz bytes.Buffer

	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}

	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	var out bytes.Buffer

	out.WriteString("!<arch>\n")
	fmt.Fprintf(&out, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", "control.tar.gz", "0", "0", "0", "100644", gz.Len())
	out.Write(gz.Bytes())

	if gz.Len()%2 == 1 {
		out.WriteByte('\n')
	}

	return out.Bytes()
}

func (e *tprEnv) debURL(p string) string {
	return "/repos/" + e.repo.Name + "/" + p
}

func (d tprDeb) poolPath() string {
	return formatdeb.GetPoolPath(tprDebComp, d.name, d.version, d.filename)
}

// uploadDeb uploads a package through the real upload handler.
func (e *tprEnv) uploadDeb(name, version string) tprDeb {
	e.t.Helper()

	d := tprDeb{
		name: name, version: version,
		filename: fmt.Sprintf("%s_%s_%s.deb", name, version, tprDebArch),
		data:     tprBuildDeb(e.t, name, version),
	}

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	fw, err := mw.CreateFormFile("file", d.filename)
	if err != nil {
		e.t.Fatalf("CreateFormFile: %v", err)
	}

	if _, err := fw.Write(d.data); err != nil {
		e.t.Fatalf("write form file: %v", err)
	}

	for field, value := range map[string]string{"distribution": tprDebDist, "component": tprDebComp} {
		if err := mw.WriteField(field, value); err != nil {
			e.t.Fatalf("WriteField(%s): %v", field, err)
		}
	}

	if err := mw.Close(); err != nil {
		e.t.Fatalf("close multipart: %v", err)
	}

	rec := e.do(http.MethodPost, "/api/v1/repositories/"+e.repo.Name+"/debs", tprDev, body.Bytes(),
		"Content-Type", mw.FormDataContentType())
	e.expect(rec, http.StatusOK)

	d.id = tprDecode[struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}](e.t, rec).Data.ID
	if d.id == "" {
		e.t.Fatalf("upload answered no package id: %s", rec.Body.String())
	}

	return d
}

// assertDebServed checks the package downloads byte-identical.
func (e *tprEnv) assertDebServed(d tprDeb) {
	e.t.Helper()

	rec := e.get(e.debURL(d.poolPath()), "")
	e.expect(rec, http.StatusOK)

	if !bytes.Equal(rec.Body.Bytes(), d.data) {
		e.t.Fatalf("GET %s differs from the uploaded bytes (%d vs %d bytes)", d.poolPath(), rec.Body.Len(), len(d.data))
	}
}

func (e *tprEnv) assertDebGone(d tprDeb) {
	e.t.Helper()

	if rec := e.get(e.debURL(d.poolPath()), ""); rec.Code != http.StatusNotFound {
		e.t.Errorf("GET %s = %d, want 404 after delete", d.poolPath(), rec.Code)
	}
}

// deleteDeb removes a package through the real delete handler.
func (e *tprEnv) deleteDeb(d tprDeb) {
	e.t.Helper()

	e.expect(e.do(http.MethodDelete, "/api/v1/repositories/"+e.repo.Name+"/debs/"+d.id, tprDev, nil), http.StatusOK)
}

func (e *tprEnv) packagesPath() string {
	return "dists/" + tprDebDist + "/" + tprDebComp + "/binary-" + tprDebArch + "/Packages"
}

// tprDebStanzas splits a Packages index into its stanzas keyed by package name.
func tprDebStanzas(index string) map[string]string {
	stanzas := make(map[string]string)

	for stanza := range strings.SplitSeq(strings.TrimSpace(index), "\n\n") {
		for line := range strings.SplitSeq(stanza, "\n") {
			if name, ok := strings.CutPrefix(line, "Package: "); ok {
				stanzas[name] = strings.TrimSpace(stanza)
			}
		}
	}

	return stanzas
}

// debIndexState reads Packages, Packages.gz and Release and reports the
// stanzas of Packages, whether Packages.gz carries the same text and whether
// Release advertises the checksum of that Packages.
func (e *tprEnv) debIndexState() (map[string]string, bool, string) {
	e.t.Helper()

	plain := e.get(e.debURL(e.packagesPath()), "")
	gzRec := e.get(e.debURL(e.packagesPath()+".gz"), "")
	release := e.get(e.debURL("dists/"+tprDebDist+"/Release"), "")

	if plain.Code != http.StatusOK || gzRec.Code != http.StatusOK || release.Code != http.StatusOK {
		return nil, false, fmt.Sprintf("Packages=%d Packages.gz=%d Release=%d", plain.Code, gzRec.Code, release.Code)
	}

	zr, err := gzip.NewReader(bytes.NewReader(gzRec.Body.Bytes()))
	if err != nil {
		return nil, false, "Packages.gz is not gzip: " + err.Error()
	}

	unzipped, err := io.ReadAll(zr)
	if err != nil {
		return nil, false, "Packages.gz read: " + err.Error()
	}

	sum := sha256.Sum256(plain.Body.Bytes())
	consistent := bytes.Equal(unzipped, plain.Body.Bytes()) &&
		strings.Contains(release.Body.String(), hex.EncodeToString(sum[:]))

	return tprDebStanzas(plain.Body.String()), consistent, plain.Body.String()
}

// waitDebIndex waits until the generated Packages index lists exactly the
// wanted packages, Packages.gz agrees and Release advertises that Packages.
// It returns the stanza of each listed package.
func (e *tprEnv) waitDebIndex(want ...string) map[string]string {
	e.t.Helper()

	var stanzas map[string]string

	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)

	tprWaitUntil(e.t, "the Packages index to list exactly "+strings.Join(wantSorted, ", "), func() (bool, string) {
		got, consistent, observed := e.debIndexState()
		if got == nil {
			return false, observed
		}

		names := make([]string, 0, len(got))
		for name := range got {
			names = append(names, name)
		}

		slices.Sort(names)
		stanzas = got

		return slices.Equal(names, wantSorted) && consistent, observed
	})

	return stanzas
}

// waitDebIndexGenerated waits until the worker stored the Packages blob, so a
// later 404 or absence cannot come from the live fallback for a repository
// that was never indexed.
func (e *tprEnv) waitDebIndexGenerated() {
	e.t.Helper()

	tprWaitUntil(e.t, "the worker to store the Packages index", func() (bool, string) {
		meta, err := harness.Database.GetRepoMetadataByPath(e.t.Context(), e.repo.ID, e.packagesPath())
		if err != nil {
			e.t.Fatalf("GetRepoMetadataByPath: %v", err)
		}

		return meta != nil, "no stored index"
	})
}

func TestTrashProtocols_Deb(t *testing.T) {
	e := tprNewEnv(t, "tpr-deb", "deb")

	hello := e.uploadDeb("tprhello", "1.0")
	world := e.uploadDeb("tprworld", "2.0")

	e.waitDebIndexGenerated()
	before := e.waitDebIndex("tprhello", "tprworld")

	e.assertDebServed(hello)
	e.assertDebServed(world)

	helloVisible := e.assertVisible("1.0")
	e.assertVisible("2.0")

	// Delete one: its download 404s and the index drops it, the other stays.
	e.deleteDeb(hello)
	e.assertDebGone(hello)
	e.assertDebServed(world)
	e.waitDebIndex("tprworld")
	e.assertHidden("1.0")
	e.assertVisible("2.0")

	// The trash lists a package as version_arch: the same version can exist per architecture.
	trashed := e.assertTrashed(models.TrashKindDeb, "1.0_"+tprDebArch, "tprhello")
	if trashed["tprhello"].ID != hello.id {
		t.Errorf("trash item id = %s, want the package id %s", trashed["tprhello"].ID, hello.id)
	}

	e.assertTrashAccess(models.TrashKindDeb, hello.id)

	// Delete the last one too: the index must not keep advertising anything.
	e.deleteDeb(world)
	e.assertDebGone(world)
	e.waitDebIndex()
	e.assertHidden("2.0")

	items := e.trashItems()
	if len(items) != 2 {
		t.Fatalf("trash holds %+v, want both packages", items)
	}

	// Restore: byte-identical downloads and the same stanzas in the index.
	e.assertRestoredOnce(e.restoreScoped(models.TrashKindDeb, hello.id), models.TrashKindDeb, hello.id)
	e.assertRestoredOnce(e.restoreGlobal(models.TrashKindDeb, world.id), models.TrashKindDeb, world.id)

	if left := e.trashItems(); len(left) != 0 {
		t.Fatalf("trash still holds %+v after restoring everything", left)
	}

	e.assertDebServed(hello)
	e.assertDebServed(world)

	after := e.waitDebIndex("tprhello", "tprworld")
	for _, name := range []string{"tprhello", "tprworld"} {
		if after[name] != before[name] {
			t.Errorf("Packages stanza of %s after restore:\n%s\nwant:\n%s", name, after[name], before[name])
		}
	}

	if got := e.assertVisible("1.0"); got != helloVisible {
		t.Errorf("visibility after restore = %+v, want %+v", got, helloVisible)
	}

	e.assertVisible("2.0")
}
