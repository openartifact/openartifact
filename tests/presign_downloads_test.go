// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/maven"
	"github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/storage"
)

const presignPublicHost = "s3.public.example:9443"

// signingStore serves bytes from a local store but signs URLs exactly as the
// S3 store does (a real S3BlobStore pointed at an unreachable internal
// endpoint, signing for the public one): signing is a local computation, so no
// object store is needed to see what a client would be redirected to.
type signingStore struct {
	storage.BlobStore

	signer *storage.S3BlobStore
}

func (s signingStore) PresignGet(ctx context.Context, sha string, ttl time.Duration, opts storage.PresignGetOptions) (string, error) {
	return s.signer.PresignGet(ctx, sha, ttl, opts)
}

func newSigningStore(t *testing.T) signingStore {
	t.Helper()

	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	s3, err := storage.NewS3BlobStore(storage.StorageConfig{
		Bucket: "artifacts", Region: "us-east-1", AccessKey: "AKIATEST", SecretKey: "secret",
		Endpoint: "http://10.255.255.1:9000", PresignEndpoint: "https://" + presignPublicHost,
		ForcePathStyle: true, PresignTTL: 3 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}

	return signingStore{BlobStore: local, signer: s3}
}

// presignFixture serves Maven and RPM behind the real RBAC middleware with the
// presign decorator installed, as cmd/server does.
type presignFixture struct {
	handler http.Handler
	cookie  *http.Cookie
	reg     *metrics.Registry
}

func newPresignFixture(t *testing.T, public bool, override *bool, settings presign.Settings) presignFixture {
	t.Helper()

	db := &cacheDB{MockDatabase: newMockDatabase()}
	_, cookieValue := setupMWUser(t, db.MockDatabase)

	repo := &models.Repository{ID: "repo-1", Name: "r", Format: "maven", Public: public, RepoType: "hosted", PresignDownloads: override}
	if err := db.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	local := newSigningStore(t)
	// An unregistered registry: NewRegistry registers on the process-wide
	// default registerer and may only be called once.
	reg := &metrics.Registry{BlobDownloadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "blob_downloads_total"}, []string{"mode", "reason"})}
	served := presign.Wrap(local, db, settings, reg, newMWLogger(t))

	digest := putBlob(t, local, "jar-bytes-0123456789")
	db.mavenArtifacts["a1"] = &models.MavenArtifact{ID: "a1", RepositoryID: repo.ID, SHA256: digest, RepoPath: "com/example/app/1.0/app-1.0.jar"}
	seedMetadata(t, db, local, repo.ID, "com/example/app/maven-metadata.xml", "<metadata/>")

	rpmDigest := putBlob(t, local, "rpm-bytes-0123456789")
	db.components["c1"] = &models.Component{ID: "c1", RepositoryID: repo.ID, SHA256: rpmDigest, Filename: "demo-1.0.x86_64.rpm"}
	seedMetadata(t, db, local, repo.ID, "repodata/repomd.xml", "<repomd/>")

	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		maven.NewHTTPHandler(newMWLogger(t), db, served).RegisterRepoRoutes(r)
		rpm.NewHTTPHandler(newMWLogger(t), db, served).RegisterRepoRoutes(r)
	})

	rbac := newRBACMiddleware(db.MockDatabase, middleware.RBACConfig{}, newMWLogger(t))

	return presignFixture{
		handler: rbac(router),
		cookie:  &http.Cookie{Name: auth.SessionCookieName, Value: cookieValue},
		reg:     reg,
	}
}

func (f presignFixture) do(t *testing.T, method, path string, authed bool) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if authed {
		req.AddCookie(f.cookie)
	}

	rw := httptest.NewRecorder()
	f.handler.ServeHTTP(rw, req)

	return rw
}

const (
	mavenJar      = "/repos/r/com/example/app/1.0/app-1.0.jar"
	mavenMetadata = "/repos/r/com/example/app/maven-metadata.xml"
	rpmPackage    = "/repos/r/Packages/demo-1.0.x86_64.rpm"
	rpmRepomd     = "/repos/r/repodata/repomd.xml"
)

func presignOn() presign.Settings {
	return presign.Settings{Downloads: true, MinSize: 0, TTL: 3 * time.Minute}
}

func TestPresignDownloads_BlobRedirectsToPublicHost(t *testing.T) {
	f := newPresignFixture(t, false, nil, presignOn())

	for _, tc := range []struct{ path, filename string }{
		{mavenJar, "app-1.0.jar"},
		{rpmPackage, "demo-1.0.x86_64.rpm"},
	} {
		rw := f.do(t, http.MethodGet, tc.path, true)
		if rw.Code != http.StatusTemporaryRedirect {
			t.Fatalf("%s: status = %d, want 307; body %q", tc.path, rw.Code, rw.Body.String())
		}

		loc, err := url.Parse(rw.Header().Get("Location"))
		if err != nil {
			t.Fatalf("%s: Location: %v", tc.path, err)
		}

		if loc.Scheme != "https" || loc.Host != presignPublicHost {
			t.Errorf("%s: Location host = %s://%s, want https://%s", tc.path, loc.Scheme, loc.Host, presignPublicHost)
		}

		q := loc.Query()
		if q.Get("X-Amz-Signature") == "" || q.Get("X-Amz-Expires") != "180" {
			t.Errorf("%s: Location is not signed for the 3m TTL: %s", tc.path, loc.RawQuery)
		}

		if cd := q.Get("response-content-disposition"); !strings.Contains(cd, tc.filename) {
			t.Errorf("%s: response-content-disposition = %q, want the file name %s", tc.path, cd, tc.filename)
		}

		if rw.Body.Len() != 0 {
			t.Errorf("%s: redirect carries a %d-byte body", tc.path, rw.Body.Len())
		}

		if cc := rw.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control = %q, want no-store", tc.path, cc)
		}
	}

	if got := testutil.ToFloat64(f.reg.BlobDownloadsTotal.WithLabelValues("redirect", "presigned")); got != 2 {
		t.Errorf("redirect counter = %v, want 2", got)
	}
}

func TestPresignDownloads_MetadataStreams(t *testing.T) {
	f := newPresignFixture(t, false, nil, presignOn())

	for _, path := range []string{mavenMetadata, rpmRepomd} {
		rw := f.do(t, http.MethodGet, path, true)
		if rw.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (metadata is never presigned)", path, rw.Code)
		}

		if loc := rw.Header().Get("Location"); loc != "" {
			t.Errorf("%s: unexpected Location %q", path, loc)
		}
	}
}

func TestPresignDownloads_UnauthorizedGetsNoLocation(t *testing.T) {
	f := newPresignFixture(t, false, nil, presignOn())

	for _, path := range []string{mavenJar, rpmPackage} {
		rw := f.do(t, http.MethodGet, path, false)
		if rw.Code != http.StatusUnauthorized && rw.Code != http.StatusForbidden {
			t.Errorf("%s: anonymous status = %d, want 401/403", path, rw.Code)
		}

		if loc := rw.Header().Get("Location"); loc != "" {
			t.Errorf("%s: unauthorised response leaks Location %q", path, loc)
		}
	}

	if got := testutil.ToFloat64(f.reg.BlobDownloadsTotal.WithLabelValues("redirect", "presigned")); got != 0 {
		t.Errorf("redirect counter = %v for unauthorised requests, want 0", got)
	}
}

func TestPresignDownloads_BelowThresholdStreams(t *testing.T) {
	s := presignOn()
	s.MinSize = 1 << 20

	f := newPresignFixture(t, false, nil, s)

	rw := f.do(t, http.MethodGet, mavenJar, true)
	if rw.Code != http.StatusOK || rw.Header().Get("Location") != "" {
		t.Fatalf("status = %d, Location = %q, want a streamed 200", rw.Code, rw.Header().Get("Location"))
	}

	if rw.Body.String() != "jar-bytes-0123456789" {
		t.Errorf("body = %q", rw.Body.String())
	}

	if got := testutil.ToFloat64(f.reg.BlobDownloadsTotal.WithLabelValues("stream", "below_min_size")); got != 1 {
		t.Errorf("stream/below_min_size counter = %v, want 1", got)
	}
}

func TestPresignDownloads_HeadIsAnsweredLocally(t *testing.T) {
	f := newPresignFixture(t, false, nil, presignOn())

	rw := f.do(t, http.MethodHead, mavenJar, true)
	if rw.Code == http.StatusTemporaryRedirect || rw.Header().Get("Location") != "" {
		t.Fatalf("HEAD answered with a redirect (%d)", rw.Code)
	}
}

func TestPresignDownloads_GlobalOffStreams(t *testing.T) {
	f := newPresignFixture(t, false, nil, presign.Settings{Downloads: false, TTL: time.Minute})

	rw := f.do(t, http.MethodGet, mavenJar, true)
	if rw.Code != http.StatusOK || rw.Header().Get("Location") != "" {
		t.Fatalf("status = %d, want 200 with presign disabled", rw.Code)
	}
}

func TestPresignDownloads_RepoOverride(t *testing.T) {
	t.Run("false overrides global on", func(t *testing.T) {
		f := newPresignFixture(t, false, new(false), presignOn())

		rw := f.do(t, http.MethodGet, mavenJar, true)
		if rw.Code != http.StatusOK || rw.Header().Get("Location") != "" {
			t.Fatalf("status = %d, want a streamed 200 for presign_downloads=false", rw.Code)
		}
	})

	t.Run("true overrides global off", func(t *testing.T) {
		f := newPresignFixture(t, false, new(true), presign.Settings{Downloads: false, TTL: time.Minute})

		rw := f.do(t, http.MethodGet, mavenJar, true)
		if rw.Code != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307 for presign_downloads=true", rw.Code)
		}
	})
}

func TestPresignDownloads_UnsupportedBackendStreams(t *testing.T) {
	// Same fixture, but the store cannot sign (like filesystem/Azure/OCI).
	db := &cacheDB{MockDatabase: newMockDatabase()}
	_, cookieValue := setupMWUser(t, db.MockDatabase)

	repo := &models.Repository{ID: "repo-1", Name: "r", Format: "maven", RepoType: "hosted"}
	if err := db.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	local, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	served := presign.Wrap(local, db, presignOn(), nil, newMWLogger(t))
	if served.Supported() {
		t.Fatal("filesystem store reports presign support")
	}

	digest := putBlob(t, local, "jar-bytes")
	db.mavenArtifacts["a1"] = &models.MavenArtifact{ID: "a1", RepositoryID: repo.ID, SHA256: digest, RepoPath: "com/example/app/1.0/app-1.0.jar"}

	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		maven.NewHTTPHandler(newMWLogger(t), db, served).RegisterRepoRoutes(r)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, mavenJar, nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieValue})

	rw := httptest.NewRecorder()
	newRBACMiddleware(db.MockDatabase, middleware.RBACConfig{}, newMWLogger(t))(router).ServeHTTP(rw, req)

	if rw.Code != http.StatusOK || rw.Body.String() != "jar-bytes" {
		t.Fatalf("status = %d body %q, want the streamed blob", rw.Code, rw.Body.String())
	}
}
