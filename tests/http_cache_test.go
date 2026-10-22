// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/format/helm"
	"github.com/openartifact/openartifact/internal/format/maven"
	"github.com/openartifact/openartifact/internal/format/npm"
	"github.com/openartifact/openartifact/internal/format/pypi"
	"github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
)

// cacheDB adds the per-format listings the cache tests need on top of the
// shared mock database.
type cacheDB struct {
	*MockDatabase

	charts []*models.HelmChart
	pypi   []*models.PyPIPackage
}

func (d *cacheDB) ListHelmCharts(context.Context, string) ([]*models.HelmChart, error) {
	return d.charts, nil
}

func (d *cacheDB) HelmIndexStamp(context.Context, string) (string, error) {
	var b strings.Builder

	for _, c := range d.charts {
		b.WriteString(c.ID + c.Digest + ";")
	}

	return b.String(), nil
}

func (d *cacheDB) ListPyPIPackages(context.Context, string) ([]*models.PyPIPackage, error) {
	return d.pypi, nil
}

func (d *cacheDB) ListPyPIPackageVersions(_ context.Context, _, name string) ([]*models.PyPIPackage, error) {
	var out []*models.PyPIPackage

	for _, p := range d.pypi {
		if p.Name == name {
			out = append(out, p)
		}
	}

	return out, nil
}

func (d *cacheDB) IsArtifactQuarantined(context.Context, string, []string) (bool, error) {
	return false, nil
}

// cacheFixture is one format served behind the real RBAC middleware.
type cacheFixture struct {
	handler http.Handler
	cookie  *http.Cookie
}

type cacheCase struct {
	name   string
	path   string
	accept string
	// seed stores the content the path serves in repository repoID.
	seed func(t *testing.T, db *cacheDB, bs storage.BlobStore, repoID string)
	// register mounts the format's routes on the /repos/{name} subrouter.
	register func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore)
}

func putBlob(t *testing.T, bs storage.BlobStore, content string) string {
	t.Helper()

	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])

	if _, err := bs.Put(context.Background(), bytes.NewReader([]byte(content)), map[string]string{"sha256": digest}); err != nil {
		t.Fatalf("Put blob: %v", err)
	}

	return digest
}

func seedMetadata(t *testing.T, db *cacheDB, bs storage.BlobStore, repoID, path, content string) {
	t.Helper()

	digest := putBlob(t, bs, content)

	if db.repoMetadata[repoID] == nil {
		db.repoMetadata[repoID] = map[string]*models.RepoMetadata{}
	}

	db.repoMetadata[repoID][path] = &models.RepoMetadata{
		ID: "meta-" + path, RepositoryID: repoID, Path: path,
		BlobSHA256: digest, BlobSize: int64(len(content)), LastGenerated: time.Now(),
	}
}

func seedMetadataFor(path, content string) func(*testing.T, *cacheDB, storage.BlobStore, string) {
	return func(t *testing.T, db *cacheDB, bs storage.BlobStore, repoID string) {
		t.Helper()

		seedMetadata(t, db, bs, repoID, path, content)
	}
}

func cacheCases() []cacheCase {
	pypiRows := []*models.PyPIPackage{{Name: "demo", Version: "1.0", Filename: "demo-1.0.tar.gz", SHA256Digest: strings.Repeat("a", 64)}}

	return []cacheCase{
		{
			name: "maven metadata", path: "/repos/r/com/example/app/maven-metadata.xml",
			seed: seedMetadataFor("com/example/app/maven-metadata.xml", "<metadata/>"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				maven.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "maven metadata sha1", path: "/repos/r/com/example/app/maven-metadata.xml.sha1",
			seed: seedMetadataFor("com/example/app/maven-metadata.xml", "<metadata/>"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				maven.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "maven jar", path: "/repos/r/com/example/app/1.0/app-1.0.jar",
			seed: func(t *testing.T, db *cacheDB, bs storage.BlobStore, repoID string) {
				t.Helper()

				digest := putBlob(t, bs, "jar-bytes")
				db.mavenArtifacts["a1"] = &models.MavenArtifact{ID: "a1", RepositoryID: repoID, SHA256: digest, RepoPath: "com/example/app/1.0/app-1.0.jar"}
			},
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				maven.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "deb Release", path: "/repos/r/dists/stable/Release",
			seed: seedMetadataFor("dists/stable/Release", "Origin: test\n"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				deb.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "deb InRelease", path: "/repos/r/dists/stable/InRelease",
			seed: seedMetadataFor("dists/stable/InRelease", "-----BEGIN PGP SIGNED MESSAGE-----\n"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				deb.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "deb Packages", path: "/repos/r/dists/stable/main/binary-amd64/Packages",
			seed: seedMetadataFor("dists/stable/main/binary-amd64/Packages", "Package: demo\n"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				deb.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "deb Packages.gz", path: "/repos/r/dists/stable/main/binary-amd64/Packages.gz",
			seed: seedMetadataFor("dists/stable/main/binary-amd64/Packages.gz", "gzbytes"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				deb.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "rpm repomd", path: "/repos/r/repodata/repomd.xml",
			seed: seedMetadataFor("repodata/repomd.xml", "<repomd/>"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				rpm.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "rpm hashed repodata", path: "/repos/r/repodata/abc123-primary.xml.gz",
			seed: seedMetadataFor("repodata/abc123-primary.xml.gz", "primary"),
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				rpm.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "rpm package", path: "/repos/r/Packages/demo-1.0.x86_64.rpm",
			seed: func(t *testing.T, db *cacheDB, bs storage.BlobStore, repoID string) {
				t.Helper()

				digest := putBlob(t, bs, "rpm-bytes")
				db.components["c1"] = &models.Component{ID: "c1", RepositoryID: repoID, SHA256: digest, Filename: "demo-1.0.x86_64.rpm"}
			},
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				rpm.NewHTTPHandler(newMWLogger(t), db, bs).RegisterRepoRoutes(r)
			},
		},
		{
			name: "helm index", path: "/repos/r/index.yaml",
			seed: func(t *testing.T, db *cacheDB, _ storage.BlobStore, repoID string) {
				t.Helper()

				db.charts = []*models.HelmChart{{ID: "h1", RepositoryID: repoID, Name: "web", Version: "1.0.0", Digest: strings.Repeat("b", 64), UpdatedAt: time.Unix(1700000000, 0)}}
			},
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				helm.NewHTTPHandler(newMWLogger(t), db, bs, "http://example.test").RegisterRepoRoutes(r)
			},
		},
		{
			name: "pypi simple index", path: "/repos/r/simple/",
			seed: func(_ *testing.T, db *cacheDB, _ storage.BlobStore, _ string) { db.pypi = pypiRows },
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				pypi.NewHTTPHandler(newMWLogger(t), db, bs, "http://example.test").RegisterRepoRoutes(r)
			},
		},
		{
			name: "pypi package index", path: "/repos/r/simple/demo/",
			seed: func(_ *testing.T, db *cacheDB, _ storage.BlobStore, _ string) { db.pypi = pypiRows },
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				pypi.NewHTTPHandler(newMWLogger(t), db, bs, "http://example.test").RegisterRepoRoutes(r)
			},
		},
		{
			name: "npm packument", path: "/repos/r/demo", accept: "application/json",
			seed: func(_ *testing.T, db *cacheDB, _ storage.BlobStore, repoID string) {
				db.npmPackages["p1"] = &models.NPMPackage{ID: "p1", RepositoryID: repoID, Name: "demo", FullName: "demo", UpdatedAt: time.Unix(1700000000, 5)}
			},
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				npm.NewHTTPHandler(newMWLogger(t), db, bs, "http://example.test").RegisterRepoRoutes(r)
			},
		},
		{
			name: "npm abbreviated packument", path: "/repos/r/demo", accept: "application/vnd.npm.install-v1+json",
			seed: func(_ *testing.T, db *cacheDB, _ storage.BlobStore, repoID string) {
				db.npmPackages["p1"] = &models.NPMPackage{ID: "p1", RepositoryID: repoID, Name: "demo", FullName: "demo", UpdatedAt: time.Unix(1700000000, 5)}
			},
			register: func(t *testing.T, r chi.Router, db *cacheDB, bs storage.BlobStore) {
				t.Helper()

				npm.NewHTTPHandler(newMWLogger(t), db, bs, "http://example.test").RegisterRepoRoutes(r)
			},
		},
	}
}

// newCacheFixture serves one format behind RBAC. The repository is public or
// private; anonymous read of private repositories is off, as in a default
// deployment.
func newCacheFixture(t *testing.T, c cacheCase, public bool) cacheFixture {
	t.Helper()

	db := &cacheDB{MockDatabase: newMockDatabase()}

	_, cookieValue := setupMWUser(t, db.MockDatabase)

	repo := &models.Repository{ID: "repo-1", Name: "r", Format: "generic", Public: public, RepoType: "hosted"}
	if err := db.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	bs, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	c.seed(t, db, bs, repo.ID)

	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) { c.register(t, r, db, bs) })

	rbac := newRBACMiddleware(db.MockDatabase, middleware.RBACConfig{}, newMWLogger(t))

	return cacheFixture{
		handler: rbac(router),
		cookie:  &http.Cookie{Name: auth.SessionCookieName, Value: cookieValue},
	}
}

func (f cacheFixture) get(t *testing.T, c cacheCase, authed bool, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, c.path, nil)
	if c.accept != "" {
		req.Header.Set("Accept", c.accept)
	}

	if authed {
		req.AddCookie(f.cookie)
	}

	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	rw := httptest.NewRecorder()
	f.handler.ServeHTTP(rw, req)

	return rw
}

func cacheControlTokens(h http.Header) []string {
	var out []string

	for part := range strings.SplitSeq(strings.Join(h.Values("Cache-Control"), ","), ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			out = append(out, part)
		}
	}

	return out
}

func hasToken(h http.Header, token string) bool {
	return slices.Contains(cacheControlTokens(h), token)
}

func varyHas(h http.Header, field string) bool {
	return strings.Contains(strings.ToLower(strings.Join(h.Values("Vary"), ",")), strings.ToLower(field))
}

// checkFirstResponse asserts the 200's validator and cache headers.
func checkFirstResponse(t *testing.T, first *httptest.ResponseRecorder, wantPublic bool) string {
	t.Helper()

	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", first.Code, first.Body.String())
	}

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("response carries no ETag")
	}

	if wantPublic != hasToken(first.Header(), "public") {
		t.Errorf("Cache-Control = %q, public wanted=%v", first.Header().Get("Cache-Control"), wantPublic)
	}

	if !wantPublic && !hasToken(first.Header(), "private") {
		t.Errorf("Cache-Control = %q, want private", first.Header().Get("Cache-Control"))
	}

	if !varyHas(first.Header(), "Authorization") {
		t.Errorf("Vary = %q, want Authorization", first.Header().Values("Vary"))
	}

	return etag
}

// checkNotModified asserts the conditional-request behaviour for etag.
func checkNotModified(t *testing.T, f cacheFixture, c cacheCase, authed bool, first *httptest.ResponseRecorder) {
	t.Helper()

	etag := first.Header().Get("ETag")

	matched := f.get(t, c, authed, etag)
	if matched.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match %s: status = %d, want 304", etag, matched.Code)
	}

	if matched.Body.Len() != 0 {
		t.Errorf("304 carries a body of %d bytes", matched.Body.Len())
	}

	if matched.Header().Get("ETag") != etag || matched.Header().Get("Cache-Control") != first.Header().Get("Cache-Control") {
		t.Errorf("304 headers = %v, want the 200's ETag and Cache-Control", matched.Header())
	}

	if weak := f.get(t, c, authed, `W/`+etag+`, "other"`); weak.Code != http.StatusNotModified {
		t.Errorf("weak/list If-None-Match: status = %d, want 304", weak.Code)
	}

	if stale := f.get(t, c, authed, `"stale"`); stale.Code != http.StatusOK {
		t.Errorf("non-matching If-None-Match: status = %d, want 200", stale.Code)
	}
}

func TestHTTPCache_Revalidation(t *testing.T) {
	// reader describes who asks and what Cache-Control must say.
	readers := []struct {
		name       string
		public     bool // repository visibility
		authed     bool
		wantPublic bool // true: "public" required; false: "private" required and "public" forbidden
	}{
		{"anonymous on public repo", true, false, true},
		{"authenticated on public repo", true, true, false},
		{"authenticated on private repo", false, true, false},
	}

	for _, c := range cacheCases() {
		for _, rd := range readers {
			t.Run(c.name+"/"+rd.name, func(t *testing.T) {
				f := newCacheFixture(t, c, rd.public)

				first := f.get(t, c, rd.authed, "")
				etag := checkFirstResponse(t, first, rd.wantPublic)

				// The validator is stable for unchanged content.
				if again := f.get(t, c, rd.authed, ""); again.Header().Get("ETag") != etag {
					t.Errorf("ETag changed between identical requests: %q vs %q", etag, again.Header().Get("ETag"))
				}

				checkNotModified(t, f, c, rd.authed, first)
			})
		}
	}
}

// A shared cache or a hostile client must never learn anything about, or get a
// 304 for, a private repository by replaying a validator: authorisation runs
// before the format handler, so the probe is refused outright.
func TestHTTPCache_UnauthenticatedProbeOfPrivateRepoNeverGets304(t *testing.T) {
	for _, c := range cacheCases() {
		t.Run(c.name, func(t *testing.T) {
			f := newCacheFixture(t, c, false)

			etag := f.get(t, c, true, "").Header().Get("ETag")
			if etag == "" {
				t.Fatal("authenticated response carries no ETag")
			}

			for _, probe := range []string{etag, "*", `W/` + etag} {
				rw := f.get(t, c, false, probe)

				if rw.Code != http.StatusUnauthorized && rw.Code != http.StatusForbidden && rw.Code != http.StatusNotFound {
					t.Errorf("anonymous If-None-Match %q: status = %d, want 401/403/404", probe, rw.Code)
				}

				if rw.Header().Get("ETag") != "" {
					t.Errorf("refused probe leaks ETag %q", rw.Header().Get("ETag"))
				}

				if hasToken(rw.Header(), "public") {
					t.Errorf("refused probe is marked public: %q", rw.Header().Get("Cache-Control"))
				}
			}
		})
	}
}
