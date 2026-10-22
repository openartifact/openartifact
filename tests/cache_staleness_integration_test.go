//go:build integration

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatcargo "github.com/openartifact/openartifact/internal/format/cargo"
	formatconda "github.com/openartifact/openartifact/internal/format/conda"
	formathelm "github.com/openartifact/openartifact/internal/format/helm"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	formatnuget "github.com/openartifact/openartifact/internal/format/nuget"
	formatrubygems "github.com/openartifact/openartifact/internal/format/rubygems"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// The metadata documents are cached under validators derived from the rows
// they are rendered from. These tests drive every cached document through the
// state changes that can alter its content, on real Postgres and the real
// format handlers, and check the two ways a cache can lie: a changed document
// must not answer 304 to the validator of the old one, and a document that did
// not change must not be reported as changed.
//
// Quarantine and repository blocking are download-time policies. Listings
// (index.yaml, packuments, repodata, ...) never filtered on them, so the
// listing stays as it was while the download is denied; the steps pin that.

const stalenessBaseURL = "http://localhost"

type staleDoc struct {
	name   string
	path   string
	accept string
}

// staleStep mutates state and lists the documents (by name) whose content must
// differ afterwards; every other document must be unchanged. after runs once
// the documents were compared.
type staleStep struct {
	name    string
	do      func()
	changes []string
	after   func()
}

type staleResp struct {
	code int
	etag string
	body string
}

type staleEnv struct {
	t      *testing.T
	pg     *database.PostgresDB
	router http.Handler
	repoID string
	repo   string
	seq    int
}

func newStaleEnv(t *testing.T, format string, mount func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler) *staleEnv {
	t.Helper()

	pg := freshPostgres(t, 5)

	bs, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	org := "00000000-0000-0000-0000-000000000001"
	days := 7
	repo := &models.Repository{Name: "stale-" + format, Format: format, RepoType: "hosted", ProjectID: &org, TrashRetentionDays: &days}

	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	return &staleEnv{t: t, pg: pg, router: mount(t, pg, bs), repoID: repo.ID, repo: repo.Name}
}

func (e *staleEnv) exec(query string, args ...any) {
	e.t.Helper()

	if _, err := e.pg.GetDB().ExecContext(context.Background(), query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

func (e *staleEnv) scan(query string, args ...any) string {
	e.t.Helper()

	var out string
	if err := e.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&out); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}

	return out
}

// blob inserts a fresh blob row and returns its id.
func (e *staleEnv) blob() string {
	e.seq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", e.repoID, e.seq)))
	sha := hex.EncodeToString(sum[:])

	return e.scan(`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1::text, 5, $1::text) RETURNING id`, sha)
}

func (e *staleEnv) url(suffix string) string { return "/repos/" + e.repo + suffix }

func (e *staleEnv) do(method, target string, body []byte, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()

	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	} else {
		rd = bytes.NewReader(nil)
	}

	req := httptest.NewRequestWithContext(e.t.Context(), method, target, rd)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func (e *staleEnv) fetch(d staleDoc, inm string) staleResp {
	e.t.Helper()

	var headers []string
	if d.accept != "" {
		headers = append(headers, "Accept", d.accept)
	}

	if inm != "" {
		headers = append(headers, "If-None-Match", inm)
	}

	rec := e.do(http.MethodGet, d.path, nil, headers...)

	return staleResp{code: rec.Code, etag: rec.Header().Get("ETag"), body: rec.Body.String()}
}

// id returns the id of the single row the query selects.
func (e *staleEnv) id(query string, args ...any) string { return e.scan(query, args...) }

func (e *staleEnv) trash(kind models.TrashKind, id string) {
	e.t.Helper()

	if soft, err := e.pg.TrashRows(context.Background(), e.repoID, kind, []string{id}, "test"); err != nil || !soft {
		e.t.Fatalf("TrashRows(%s) = %v, %v; want a soft delete", kind, soft, err)
	}
}

func (e *staleEnv) restore(kind models.TrashKind, id string) {
	e.t.Helper()

	if _, err := e.pg.RestoreTrash(context.Background(), kind, id); err != nil {
		e.t.Fatalf("RestoreTrash(%s): %v", kind, err)
	}
}

// quarantine flags (or clears) a download key the way a scan policy does.
func (e *staleEnv) quarantine(artifact string, on bool) {
	e.t.Helper()

	ctx := context.Background()

	res := &models.VulnScanResult{
		RepositoryID: e.repoID, ArtifactID: artifact, PackageName: "pkg", PackageVersion: "1", Ecosystem: "test",
		Scanner: "osv", ScanStatus: "completed", Quarantined: true,
	}

	if err := e.pg.CreateVulnScanResult(ctx, res); err != nil {
		e.t.Fatalf("CreateVulnScanResult: %v", err)
	}

	if err := e.pg.QuarantineVulnArtifact(ctx, e.repoID, artifact, on); err != nil {
		e.t.Fatalf("QuarantineVulnArtifact: %v", err)
	}
}

func (e *staleEnv) block(on bool) {
	e.t.Helper()

	ctx := context.Background()

	repo, err := e.pg.GetRepositoryByName(ctx, e.repo)
	if err != nil || repo == nil {
		e.t.Fatalf("GetRepositoryByName: %v", err)
	}

	repo.Blocked = on
	if err := e.pg.UpdateRepository(ctx, repo); err != nil {
		e.t.Fatalf("UpdateRepository: %v", err)
	}
}

// denied returns an after hook asserting that GET target answers 403 while the
// artifact is quarantined, and anything but 403 once it is not.
func (e *staleEnv) denied(target string, want bool) func() {
	return func() {
		e.t.Helper()

		rec := e.do(http.MethodGet, target, nil)
		if got := rec.Code == http.StatusForbidden; got != want {
			e.t.Errorf("GET %s = %d, want denied=%v", target, rec.Code, want)
		}
	}
}

// quarantineSteps are the policy steps every format shares: listing unchanged,
// download denied, and the same for a blocked hosted repository.
func (e *staleEnv) policySteps(artifact, download string) []staleStep {
	return []staleStep{
		{name: "quarantine", do: func() { e.quarantine(artifact, true) }, after: e.denied(download, true)},
		{name: "unquarantine", do: func() { e.quarantine(artifact, false) }, after: e.denied(download, false)},
		{name: "block repository", do: func() { e.block(true) }},
		{name: "unblock repository", do: func() { e.block(false) }},
	}
}

// run applies the steps in order, comparing every document before and after.
func (e *staleEnv) run(docs []staleDoc, steps []staleStep) {
	e.t.Helper()

	for _, s := range steps {
		before := make(map[string]staleResp, len(docs))

		for _, d := range docs {
			r := e.fetch(d, "")
			if r.code != http.StatusOK || r.etag == "" {
				e.t.Fatalf("before %q: GET %s = %d (ETag %q): %s", s.name, d.path, r.code, r.etag, r.body)
			}

			before[d.name] = r
		}

		s.do()

		for _, d := range docs {
			e.compare(s, d, before[d.name])
		}

		if s.after != nil {
			s.after()
		}
	}
}

func (e *staleEnv) compare(s staleStep, d staleDoc, before staleResp) {
	e.t.Helper()

	after := e.fetch(d, before.etag)

	if slices.Contains(s.changes, d.name) {
		switch {
		case after.code == http.StatusNotModified:
			e.t.Errorf("%s: %s answered 304 to the old validator %s although it changed", s.name, d.name, before.etag)
		case after.code != http.StatusOK:
			e.t.Errorf("%s: %s = %d", s.name, d.name, after.code)
		case after.body == before.body:
			e.t.Errorf("%s: %s is unchanged (stale)", s.name, d.name)
		case after.etag == before.etag:
			e.t.Errorf("%s: %s changed under the same validator %s", s.name, d.name, before.etag)
		}

		return
	}

	if after.code != http.StatusNotModified && after.body != before.body {
		e.t.Errorf("%s: %s changed (status %d) but must not", s.name, d.name, after.code)
	}
}

func (e *staleEnv) withBlob(sql string, args ...any) {
	e.t.Helper()

	e.exec(sql, append(args, e.blob())...)
}

// --- mounts --------------------------------------------------------------

func mountRepoRoutes(register func(log *logger.Logger, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router)) func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler {
	return func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
		t.Helper()

		router := chi.NewRouter()
		router.Route("/repos/{name}", register(newTestLogger(t), pg, bs))

		return router
	}
}

// --- helm ----------------------------------------------------------------

func TestCachedIndexStaysCurrentHelm(t *testing.T) {
	e := newStaleEnv(t, "helm", mountRepoRoutes(func(log *logger.Logger, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		return formathelm.NewHTTPHandler(log, pg, bs, stalenessBaseURL).RegisterRepoRoutes
	}))

	add := func(version string) func() {
		return func() {
			e.withBlob(`INSERT INTO helm_charts (repository_id, name, version, blob_id) VALUES ($1,'web',$2,$3)`, e.repoID, version)
		}
	}
	chart := func() string {
		return e.id(`SELECT id FROM helm_charts WHERE repository_id = $1 AND version = '1.1.0'`, e.repoID)
	}

	add("1.0.0")()

	steps := []staleStep{
		{name: "publish", do: add("1.1.0"), changes: []string{"index"}},
	}
	steps = append(steps, e.policySteps("web-1.1.0.tgz", e.url("/charts/web-1.1.0.tgz"))...)
	steps = append(steps,
		staleStep{name: "trash", do: func() { e.trash(models.TrashKindHelm, chart()) }, changes: []string{"index"}},
		staleStep{name: "restore", do: func() { e.restore(models.TrashKindHelm, chart()) }, changes: []string{"index"}},
		staleStep{name: "hard delete", do: func() {
			e.exec(`DELETE FROM helm_charts WHERE repository_id = $1 AND version = '1.1.0'`, e.repoID)
		}, changes: []string{"index"}},
	)

	e.run([]staleDoc{{name: "index", path: e.url("/index.yaml")}}, steps)
}

// --- conda ---------------------------------------------------------------

func TestCachedRepodataStaysCurrentConda(t *testing.T) {
	e := newStaleEnv(t, "conda", mountRepoRoutes(func(log *logger.Logger, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		return formatconda.NewHTTPHandler(log, pg, bs, stalenessBaseURL).RegisterRepoRoutes
	}))

	add := func(platform, name string) func() {
		return func() {
			e.withBlob(`INSERT INTO conda_packages (repository_id, name, version, build_string, platform, depends, blob_id, extension, build_number, md5, timestamp_ms, index_extra)
			            VALUES ($1,$2,'1.0','h1',$3,'{}',$4,'.conda',0,'m',1,'{}')`, e.repoID, name, platform)
		}
	}
	pkg := func() string {
		return e.id(`SELECT id FROM conda_packages WHERE repository_id = $1 AND name = 'numpy'`, e.repoID)
	}

	add("linux-64", "base")()
	add("noarch", "base")()

	docs := []staleDoc{
		{name: "linux-64", path: e.url("/linux-64/repodata.json")},
		{name: "noarch", path: e.url("/noarch/repodata.json")},
	}

	steps := []staleStep{
		{name: "publish on linux-64", do: add("linux-64", "numpy"), changes: []string{"linux-64"}},
	}
	steps = append(steps, e.policySteps("numpy-1.0-h1.conda", e.url("/linux-64/numpy-1.0-h1.conda"))...)
	steps = append(steps,
		staleStep{name: "trash", do: func() { e.trash(models.TrashKindConda, pkg()) }, changes: []string{"linux-64"}},
		staleStep{name: "restore", do: func() { e.restore(models.TrashKindConda, pkg()) }, changes: []string{"linux-64"}},
		staleStep{name: "publish on noarch", do: add("noarch", "numpy"), changes: []string{"noarch"}},
	)

	e.run(docs, steps)
}

// --- rubygems ------------------------------------------------------------

func TestCachedSpecsStayCurrentRubyGems(t *testing.T) {
	e := newStaleEnv(t, "rubygems", mountRepoRoutes(func(log *logger.Logger, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		return formatrubygems.NewHTTPHandler(log, pg, bs, stalenessBaseURL).RegisterRepoRoutes
	}))

	add := func(version string) func() {
		return func() {
			e.withBlob(`INSERT INTO rubygems_gems (repository_id, name, version, platform, blob_id, size) VALUES ($1,'rake',$2,'ruby',$3,5)`, e.repoID, version)
		}
	}
	gem := func() string {
		return e.id(`SELECT id FROM rubygems_gems WHERE repository_id = $1 AND version = '1.0'`, e.repoID)
	}
	setYanked := func(version string, yanked bool) {
		if err := e.pg.YankRubyGem(context.Background(), e.repoID, "rake", version, "ruby", yanked); err != nil {
			t.Fatalf("YankRubyGem(%s, %v): %v", version, yanked, err)
		}
	}
	yank := func() {
		form := url.Values{"gem_name": {"rake"}, "version": {"1.0"}}.Encode()

		rec := e.do(http.MethodDelete, e.url("/api/v1/gems/yank"), []byte(form), "Content-Type", "application/x-www-form-urlencoded")
		if rec.Code != http.StatusOK {
			t.Fatalf("yank = %d: %s", rec.Code, rec.Body.String())
		}
	}

	docs := []staleDoc{
		{name: "specs", path: e.url("/specs.4.8.gz")},
		{name: "latest", path: e.url("/latest_specs.4.8.gz")},
		{name: "prerelease", path: e.url("/prerelease_specs.4.8.gz")},
	}

	add("0.9")()
	add("1.0")()
	e.exec(`UPDATE rubygems_gems SET version = '1.0.pre' WHERE repository_id = $1 AND version = '0.9'`, e.repoID)

	steps := []staleStep{
		{name: "publish", do: add("2.0"), changes: []string{"specs", "latest"}},
		{name: "yank the latest version", do: func() { setYanked("2.0", true) }, changes: []string{"specs", "latest"}},
		{name: "unyank the latest version", do: func() { setYanked("2.0", false) }, changes: []string{"specs", "latest"}},
		{name: "yank", do: yank, changes: []string{"specs"}},
		{name: "unyank", do: func() { setYanked("1.0", false) }, changes: []string{"specs"}},
	}
	steps = append(steps, e.policySteps("rake-1.0.gem", e.url("/gems/rake-1.0.gem"))...)
	steps = append(steps,
		staleStep{name: "trash", do: func() { e.trash(models.TrashKindRubyGems, gem()) }, changes: []string{"specs"}},
		staleStep{name: "restore", do: func() { e.restore(models.TrashKindRubyGems, gem()) }, changes: []string{"specs"}},
	)

	e.run(docs, steps)
}

// --- cargo ---------------------------------------------------------------

func TestCachedIndexStaysCurrentCargo(t *testing.T) {
	e := newStaleEnv(t, "cargo", mountRepoRoutes(func(log *logger.Logger, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		return formatcargo.NewHTTPHandler(log, pg, bs, stalenessBaseURL).RegisterRepoRoutes
	}))

	add := func(version string) func() {
		return func() {
			e.withBlob(`INSERT INTO cargo_crates (repository_id, name, version, metadata_json, blob_id) VALUES ($1,'serde',$2,'{}',$3)`, e.repoID, version)
		}
	}
	crate := func() string {
		return e.id(`SELECT id FROM cargo_crates WHERE repository_id = $1 AND version = '1.1.0'`, e.repoID)
	}
	setYanked := func(method, verb string) func() {
		return func() {
			rec := e.do(method, e.url("/api/v1/crates/serde/1.1.0/"+verb), nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s = %d: %s", verb, rec.Code, rec.Body.String())
			}
		}
	}

	add("1.0.0")()

	steps := []staleStep{
		{name: "publish", do: add("1.1.0"), changes: []string{"index"}},
		{name: "yank", do: setYanked(http.MethodDelete, "yank"), changes: []string{"index"}},
		{name: "unyank", do: setYanked(http.MethodPut, "unyank"), changes: []string{"index"}},
		{name: "download count", do: func() {
			e.exec(`UPDATE cargo_crates SET download_count = download_count + 1, updated_at = now() WHERE repository_id = $1`, e.repoID)
		}},
	}
	steps = append(steps, e.policySteps("serde-1.1.0.crate", e.url("/api/v1/crates/serde/1.1.0/download"))...)
	steps = append(steps,
		staleStep{name: "trash", do: func() { e.trash(models.TrashKindCargo, crate()) }, changes: []string{"index"}},
		staleStep{name: "restore", do: func() { e.restore(models.TrashKindCargo, crate()) }, changes: []string{"index"}},
	)

	e.run([]staleDoc{{name: "index", path: e.url("/index/se/rd/serde")}}, steps)
}

// --- nuget ---------------------------------------------------------------

func TestCachedRegistrationStaysCurrentNuGet(t *testing.T) {
	e := newStaleEnv(t, "nuget", mountRepoRoutes(func(log *logger.Logger, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		return formatnuget.NewHTTPHandler(log, pg, bs, stalenessBaseURL).RegisterRepoRoutes
	}))

	add := func(version string) func() {
		return func() {
			sum := sha256.Sum256([]byte(e.repoID + version))
			sha := hex.EncodeToString(sum[:])

			pkg := &models.NuGetPackage{
				RepositoryID: e.repoID, PackageID: "contoso.lib", Version: version, DependencyGroups: json.RawMessage("[]"),
				BlobKey: sha, PackageHash: sha, PackageHashAlgorithm: "SHA512", PackageSize: 5,
			}
			if err := e.pg.CreateNuGetPackage(context.Background(), pkg); err != nil {
				t.Fatalf("CreateNuGetPackage(%s): %v", version, err)
			}
		}
	}
	pkg := func() string {
		return e.id(`SELECT id FROM nuget_packages WHERE repository_id = $1 AND version = '2.0.0'`, e.repoID)
	}

	add("1.0.0")()

	steps := []staleStep{
		{name: "publish", do: add("2.0.0"), changes: []string{"registration"}},
	}
	steps = append(steps, e.policySteps("contoso.lib.2.0.0.nupkg", e.url("/v3/package/contoso.lib/2.0.0/contoso.lib.2.0.0.nupkg"))...)
	steps = append(steps,
		staleStep{name: "trash", do: func() { e.trash(models.TrashKindNuGet, pkg()) }, changes: []string{"registration"}},
		staleStep{name: "restore", do: func() { e.restore(models.TrashKindNuGet, pkg()) }, changes: []string{"registration"}},
		staleStep{name: "delete", do: func() {
			if rec := e.do(http.MethodDelete, e.url("/v3/package/contoso.lib/2.0.0"), nil); rec.Code >= 300 {
				t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
			}
		}, changes: []string{"registration"}},
	)

	e.run([]staleDoc{{name: "registration", path: e.url("/v3/registration/contoso.lib/index.json")}}, steps)
}

// --- maven ---------------------------------------------------------------

// maven-metadata.xml and its checksums are keyed by the digest of the metadata
// blob, so a regenerated document always arrives under a new validator.
func TestCachedMetadataStaysCurrentMaven(t *testing.T) {
	var bs storage.BlobStore

	e := newStaleEnv(t, "maven", func(t *testing.T, pg *database.PostgresDB, store storage.BlobStore) http.Handler {
		t.Helper()

		bs = store
		router := chi.NewRouter()
		router.Route("/repos/maven/{name}", formatmaven.NewHTTPHandler(newTestLogger(t), pg, store).RegisterRepoRoutes)

		return router
	})

	const metaPath = "com/acme/lib/maven-metadata.xml"

	publish := func(versions ...string) func() {
		return func() {
			var sb strings.Builder

			sb.WriteString("<metadata><versioning><versions>")

			for _, v := range versions {
				sb.WriteString("<version>" + v + "</version>")
			}

			sb.WriteString("</versions></versioning></metadata>")

			xml := sb.String()

			sum := sha256.Sum256([]byte(xml))
			sha := hex.EncodeToString(sum[:])

			if _, err := bs.Put(context.Background(), strings.NewReader(xml), map[string]string{"sha256": sha}); err != nil {
				t.Fatalf("Put: %v", err)
			}

			err := e.pg.UpsertRepoMetadata(context.Background(), &models.RepoMetadata{
				RepositoryID: e.repoID, Path: metaPath, BlobSHA256: sha, BlobSize: int64(len(xml)),
			})
			if err != nil {
				t.Fatalf("UpsertRepoMetadata: %v", err)
			}
		}
	}

	base := "/repos/maven/" + e.repo + "/" + metaPath
	docs := []staleDoc{
		{name: "xml", path: base},
		{name: "sha1", path: base + ".sha1"},
		{name: "md5", path: base + ".md5"},
		{name: "sha256", path: base + ".sha256"},
		{name: "sha512", path: base + ".sha512"},
	}
	all := []string{"xml", "sha1", "md5", "sha256", "sha512"}

	publish("1.0")()

	e.run(docs, []staleStep{
		{name: "regenerated with a new version", do: publish("1.0", "1.1"), changes: all},
		{name: "regenerated without a version", do: publish("1.1"), changes: all},
		{name: "regenerated identically", do: publish("1.1")},
	})
}

// --- npm -----------------------------------------------------------------

func TestCachedPackumentStaysCurrentNPM(t *testing.T) {
	e := newStaleEnv(t, "npm", func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
		t.Helper()

		log := newTestLogger(t)
		router := chi.NewRouter()
		router.Use(authenticateTestBearer)
		router.Route("/repos/{name}", func(r chi.Router) {
			formatnpm.NewHTTPHandler(log, pg, bs, stalenessBaseURL).RegisterRepoRoutes(r)
			api.NewNPMUploadHandler(log, pg, bs, stalenessBaseURL, nil).RegisterRepoRoutes(r)
		})

		return router
	})

	bearer := []string{"Authorization", "Bearer token", "Content-Type", "application/json"}
	call := func(method, target string, body []byte) {
		t.Helper()

		if rec := e.do(method, target, body, bearer...); rec.Code >= 300 {
			t.Fatalf("%s %s = %d: %s", method, target, rec.Code, rec.Body.String())
		}
	}
	publish := func(version string) func() {
		return func() { call(http.MethodPut, e.url("/demo"), buildPublishBody(t, "demo", version)) }
	}
	pkgID := func() string {
		return e.id(`SELECT id FROM npm_packages WHERE repository_id = $1 AND full_name = 'demo'`, e.repoID)
	}
	version := func(v string) string {
		return e.id(`SELECT id FROM npm_versions WHERE package_id = $1 AND version = $2`, pkgID(), v)
	}
	distTag := func(method, tag string) func() {
		return func() { call(method, e.url("/-/package/demo/dist-tags/"+tag), []byte(`"1.0.0"`)) }
	}
	deprecate := func() {
		var doc map[string]any

		rec := e.do(http.MethodGet, e.url("/demo"), nil)
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("decode packument: %v", err)
		}

		versions, _ := doc["versions"].(map[string]any)
		manifest, _ := versions["1.0.0"].(map[string]any)
		manifest["deprecated"] = "use 1.1.0"

		body, _ := json.Marshal(doc)
		call(http.MethodPut, e.url("/demo"), body)
	}

	publish("1.0.0")()

	docs := []staleDoc{
		{name: "full", path: e.url("/demo")},
		{name: "abbreviated", path: e.url("/demo"), accept: "application/vnd.npm.install-v1+json"},
	}
	both := []string{"full", "abbreviated"}

	steps := []staleStep{
		{name: "publish", do: publish("1.1.0"), changes: both},
		{name: "dist-tag put", do: distTag(http.MethodPut, "beta"), changes: both},
		{name: "dist-tag delete", do: distTag(http.MethodDelete, "beta"), changes: both},
		{name: "deprecate", do: deprecate, changes: both},
		// Writers that do not go through the upload handler (backup restore,
		// maintenance scripts) must move the validator too.
		{name: "dist-tag upsert in the database", do: func() {
			if _, err := e.pg.UpsertNPMDistTag(context.Background(), &models.NPMDistTag{PackageID: pkgID(), Tag: "next", Version: "1.1.0"}); err != nil {
				t.Fatalf("UpsertNPMDistTag: %v", err)
			}
		}, changes: both},
		{name: "dist-tag delete in the database", do: func() {
			if err := e.pg.DeleteNPMDistTag(context.Background(), pkgID(), "next"); err != nil {
				t.Fatalf("DeleteNPMDistTag: %v", err)
			}
		}, changes: both},
		{name: "manifest edit in the database", do: func() {
			manifest := `{"name":"demo","version":"1.1.0","description":"edited","dist":{"tarball":"http://example.com/demo-1.1.0.tgz"}}`
			if err := e.pg.UpdateNPMVersionManifest(context.Background(), pkgID(), "1.1.0", []byte(manifest)); err != nil {
				t.Fatalf("UpdateNPMVersionManifest: %v", err)
			}
		}, changes: []string{"full"}},
	}
	steps = append(steps, e.policySteps("demo-1.1.0.tgz", e.url("/demo/-/demo-1.1.0.tgz"))...)
	steps = append(steps,
		staleStep{name: "trash", do: func() { e.trash(models.TrashKindNPM, version("1.1.0")) }, changes: both},
		staleStep{name: "restore", do: func() { e.restore(models.TrashKindNPM, version("1.1.0")) }, changes: both},
		staleStep{name: "unpublish", do: func() {
			call(http.MethodDelete, e.url("/demo/-/demo-1.1.0.tgz/-rev/1-x"), nil)
		}, changes: both},
		staleStep{name: "version delete in the database", do: func() {
			publish("2.0.0")()

			if err := e.pg.DeleteNPMVersion(context.Background(), pkgID(), "1.0.0"); err != nil {
				t.Fatalf("DeleteNPMVersion: %v", err)
			}
		}, changes: both},
	)

	e.run(docs, steps)
}
