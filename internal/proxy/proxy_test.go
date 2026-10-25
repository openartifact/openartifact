// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const allowPrivateEnv = "OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS"

// memDB is the slice of the database the proxy service touches.
type memDB struct {
	database.BaseMockDB

	mu      sync.Mutex
	entries map[string]*models.ProxyCacheEntry
	blobs   map[string]int64
}

func newMemDB() *memDB {
	return &memDB{entries: map[string]*models.ProxyCacheEntry{}, blobs: map[string]int64{}}
}

func (d *memDB) GetProxyCacheEntry(_ context.Context, repoID, key string) (*models.ProxyCacheEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	e, ok := d.entries[repoID+"|"+key]
	if !ok {
		return nil, nil
	}

	cp := *e

	return &cp, nil
}

func (d *memDB) UpsertProxyCacheEntry(_ context.Context, e *models.ProxyCacheEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	cp := *e
	d.entries[e.RepositoryID+"|"+e.CacheKey] = &cp

	return nil
}

func (d *memDB) DeleteProxyCacheEntry(_ context.Context, repoID, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.entries, repoID+"|"+key)

	return nil
}

func (d *memDB) EnsureBlob(_ context.Context, sha, _ string, size int64) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.blobs[sha] = size

	return sha, nil
}

// expire moves an entry's expiry into the past.
func (d *memDB) expire(repoID, key string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.entries[repoID+"|"+key].ExpiresAt = time.Now().Add(-time.Minute)
}

// expireAgo makes an entry expire ago in the past.
func (d *memDB) expireAgo(repoID, key string, ago time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.entries[repoID+"|"+key].ExpiresAt = time.Now().Add(-ago)
}

func (d *memDB) entry(repoID, key string) *models.ProxyCacheEntry {
	e, _ := d.GetProxyCacheEntry(context.Background(), repoID, key)
	return e
}

type fixture struct {
	t    *testing.T
	svc  *Service
	db   *memDB
	repo *models.Repository
	hits atomic.Int32
}

// newFixture serves upstream through a maven proxy repository. handler is
// wrapped to count upstream requests.
func newFixture(t *testing.T, handler http.HandlerFunc) *fixture {
	t.Helper()
	t.Setenv(allowPrivateEnv, "true") // httptest upstreams are loopback

	f := &fixture{t: t, db: newMemDB()}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}

	f.svc = NewService(log, f.db, store)
	f.repo = &models.Repository{
		ID:                      "repo-1",
		Name:                    "central",
		Format:                  "maven",
		RepoType:                "proxy",
		UpstreamURL:             upstream.URL + "/maven2/",
		CacheTTLSeconds:         600,
		NegativeCacheTTLSeconds: 60,
		MaxStaleSeconds:         3600,
	}

	return f
}

func (f *fixture) do(method, path string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	return w
}

func okBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/java-archive")
		_, _ = io.WriteString(w, body)
	}
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestServe_MissFetchesUpstreamThenHitServesCache(t *testing.T) {
	f := newFixture(t, okBody("jar-bytes"))

	first := f.do(http.MethodGet, "junit/junit/4.13.2/junit-4.13.2.jar")
	if first.Code != http.StatusOK || first.Body.String() != "jar-bytes" {
		t.Fatalf("miss: status %d body %q", first.Code, first.Body.String())
	}

	second := f.do(http.MethodGet, "junit/junit/4.13.2/junit-4.13.2.jar")
	if second.Code != http.StatusOK || second.Body.String() != "jar-bytes" {
		t.Fatalf("hit: status %d body %q", second.Code, second.Body.String())
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1 (second request must come from cache)", got)
	}

	e := f.db.entry("repo-1", "junit/junit/4.13.2/junit-4.13.2.jar")
	if e == nil || e.BlobKey != sum("jar-bytes") || e.SizeBytes != int64(len("jar-bytes")) {
		t.Fatalf("cache entry not recorded with content hash: %+v", e)
	}

	if f.db.blobs[sum("jar-bytes")] != int64(len("jar-bytes")) {
		t.Error("blob row not ensured, refcounting would treat the blob as unreferenced")
	}
}

func TestServe_UpstreamPathIsAppendedToBase(t *testing.T) {
	var gotPath string

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "x")
	})

	f.do(http.MethodGet, "/org/foo/1.0/foo-1.0.pom")

	if gotPath != "/maven2/org/foo/1.0/foo-1.0.pom" {
		t.Errorf("upstream path = %q", gotPath)
	}
}

func TestServe_HeadOnMissFillsCacheAndSendsNoBody(t *testing.T) {
	f := newFixture(t, okBody("0123456789"))

	w := f.do(http.MethodHead, "a/b/c.jar")
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD: status %d len %d CL %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
	}

	if f.db.entry("repo-1", "a/b/c.jar") == nil {
		t.Error("HEAD miss did not populate the cache")
	}
}

func TestServe_MutablePathExpiresByTTLAndRefetches(t *testing.T) {
	f := newFixture(t, okBody("<metadata/>"))

	f.do(http.MethodGet, "g/a/maven-metadata.xml")
	f.do(http.MethodGet, "g/a/maven-metadata.xml")

	if got := f.hits.Load(); got != 1 {
		t.Fatalf("within TTL: upstream requests = %d, want 1", got)
	}

	f.db.expire("repo-1", "g/a/maven-metadata.xml")
	f.do(http.MethodGet, "g/a/maven-metadata.xml")

	if got := f.hits.Load(); got != 2 {
		t.Errorf("after TTL: upstream requests = %d, want 2", got)
	}
}

func TestServe_ReleasedArtifactIsCachedAsImmutable(t *testing.T) {
	f := newFixture(t, okBody("jar"))

	f.do(http.MethodGet, "g/a/1.0/a-1.0.jar")
	f.do(http.MethodGet, "g/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.jar")

	released := f.db.entry("repo-1", "g/a/1.0/a-1.0.jar")
	snapshot := f.db.entry("repo-1", "g/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.jar")

	if time.Until(released.ExpiresAt) < 50*365*24*time.Hour {
		t.Errorf("released artifact expires in %v, want effectively never", time.Until(released.ExpiresAt))
	}

	if time.Until(snapshot.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("snapshot expires in %v, want within the repo TTL", time.Until(snapshot.ExpiresAt))
	}
}

// A close-delimited body (no Content-Length, not chunked) cut by a clean FIN is
// indistinguishable from a complete one, so a release must not be pinned for a
// century (STOR-10).
func TestServe_CloseDelimitedBodyIsNotCachedAsImmutable(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}

		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: application/java-archive\r\n\r\njar")
		_ = buf.Flush()
		_ = conn.Close()
	})

	f.do(http.MethodGet, "g/a/1.0/a-1.0.jar")

	e := f.db.entry("repo-1", "g/a/1.0/a-1.0.jar")
	if e == nil {
		t.Fatal("no cache entry")
	}

	if time.Until(e.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("close-delimited release expires in %v, want within the repo TTL", time.Until(e.ExpiresAt))
	}
}

func TestServe_NegativeCache(t *testing.T) {
	f := newFixture(t, http.NotFound)

	for range 3 {
		if w := f.do(http.MethodGet, "g/a/9.9/a-9.9.jar"); w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1 (404 must be negatively cached)", got)
	}

	f.db.expire("repo-1", "g/a/9.9/a-9.9.jar")
	f.do(http.MethodGet, "g/a/9.9/a-9.9.jar")

	if got := f.hits.Load(); got != 2 {
		t.Errorf("after negative TTL: upstream requests = %d, want 2", got)
	}
}

func TestServe_NegativeCacheDisabledWhenTTLZero(t *testing.T) {
	f := newFixture(t, http.NotFound)
	f.repo.NegativeCacheTTLSeconds = 0

	f.do(http.MethodGet, "x/y.jar")
	f.do(http.MethodGet, "x/y.jar")

	if got := f.hits.Load(); got != 2 {
		t.Errorf("upstream requests = %d, want 2", got)
	}
}

func TestServe_UpstreamErrorsAre502AndNotCached(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })

		w := f.do(http.MethodGet, "g/a/1.0/a-1.0.jar")
		if w.Code != http.StatusBadGateway {
			t.Errorf("upstream %d: status = %d, want 502", status, w.Code)
		}

		if body := w.Body.String(); body != "upstream repository error\n" {
			t.Errorf("upstream %d: body %q leaks detail", status, body)
		}

		if f.db.entry("repo-1", "g/a/1.0/a-1.0.jar") != nil {
			t.Errorf("upstream %d: error response was cached", status)
		}
	}
}

func TestServe_UnreachableUpstreamIs502(t *testing.T) {
	f := newFixture(t, okBody("x"))
	f.repo.UpstreamURL = "http://127.0.0.1:1/"

	if w := f.do(http.MethodGet, "a.jar"); w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestServe_OversizedUpstreamBodyIs502(t *testing.T) {
	f := newFixture(t, okBody("0123456789-too-long"))
	f.svc.maxBody = 10

	if w := f.do(http.MethodGet, "big.bin"); w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}

	if f.db.entry("repo-1", "big.bin") != nil {
		t.Error("oversized body was cached")
	}
}

func TestServe_PrivateUpstreamBlockedByDefault(t *testing.T) {
	f := newFixture(t, okBody("x"))
	t.Setenv(allowPrivateEnv, "")

	if w := f.do(http.MethodGet, "a.jar"); w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}

	if got := f.hits.Load(); got != 0 {
		t.Errorf("loopback upstream was contacted %d times", got)
	}
}

func TestServe_SendsDecryptedBasicAuth(t *testing.T) {
	var user, pass string

	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		_, _ = io.WriteString(w, "x")
	})
	f.repo.UpstreamUsername = "ci"
	f.repo.UpstreamPasswordEnc = "s3cret" // legacy plaintext values decrypt to themselves

	f.do(http.MethodGet, "a.jar")

	if user != "ci" || pass != "s3cret" {
		t.Errorf("upstream saw credentials %q/%q", user, pass)
	}
}

func TestServe_RejectsWritesBlockedAndBadPaths(t *testing.T) {
	f := newFixture(t, okBody("x"))

	if w := f.do(http.MethodPut, "a.jar"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT status = %d, want 405", w.Code)
	}

	for _, p := range []string{"", "/", "dir/", "../etc/passwd", "a/../b", "a//b", "a/./b", `a\b`, "a\x00b"} {
		if w := f.do(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("path %q: status = %d, want 404", p, w.Code)
		}
	}

	f.repo.Blocked = true

	if w := f.do(http.MethodGet, "a.jar"); w.Code != http.StatusForbidden {
		t.Errorf("blocked status = %d, want 403", w.Code)
	}

	if got := f.hits.Load(); got != 0 {
		t.Errorf("upstream contacted %d times for refused requests", got)
	}
}

func TestServe_ConditionalGetReturns304(t *testing.T) {
	f := newFixture(t, okBody("jar"))

	first := f.do(http.MethodGet, "a.jar")
	etag := first.Header().Get("ETag")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, "a.jar")

	if etag == "" || w.Code != http.StatusNotModified {
		t.Errorf("etag %q, conditional status = %d, want 304", etag, w.Code)
	}
}

// missingBlobStore reports every blob as absent until restore is called.
type missingBlobStore struct {
	storage.BlobStore

	missing atomic.Bool
}

func (s *missingBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if s.missing.Load() {
		return nil, errors.New("blob missing")
	}

	return s.BlobStore.Get(ctx, id)
}

func TestServe_CacheRowWithoutBlobIsRefetched(t *testing.T) {
	f := newFixture(t, okBody("jar"))
	store := &missingBlobStore{BlobStore: f.svc.blobStore}
	f.svc.blobStore = store

	f.do(http.MethodGet, "a.jar")
	store.missing.Store(true)

	// Get keeps failing, so the refetch cannot be served, but it must have
	// been attempted rather than serving a dangling row.
	if w := f.do(http.MethodGet, "a.jar"); w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when storage is broken", w.Code)
	}

	if got := f.hits.Load(); got != 2 {
		t.Errorf("upstream requests = %d, want 2 (refetch after blob loss)", got)
	}
}

func TestServe_ConcurrentMissesSingleFlight(t *testing.T) {
	const clients = 8

	release := make(chan struct{})
	arrived := make(chan struct{}, clients)

	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}

		<-release

		_, _ = io.WriteString(w, "shared-bytes")
	})

	key := f.repo.ID + "\x00" + "g/a/1.0/a-1.0.jar"
	codes := make(chan int, clients)
	bodies := make(chan string, clients)

	var wg sync.WaitGroup

	for range clients {
		wg.Go(func() {
			w := f.do(http.MethodGet, "g/a/1.0/a-1.0.jar")
			codes <- w.Code
			bodies <- w.Body.String()
		})
	}

	<-arrived

	deadline := time.Now().Add(10 * time.Second)
	for f.svc.flights.waiting(key) < clients-1 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d requests joined the in-flight fetch", f.svc.flights.waiting(key), clients-1)
		}

		runtime.Gosched()
	}

	close(release)
	wg.Wait()
	close(codes)
	close(bodies)

	for c := range codes {
		if c != http.StatusOK {
			t.Errorf("status = %d, want 200", c)
		}
	}

	for b := range bodies {
		if b != "shared-bytes" {
			t.Errorf("body = %q", b)
		}
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1", got)
	}
}

func TestImmutable(t *testing.T) {
	cases := []struct {
		format, key string
		want        bool
	}{
		{"maven", "g/a/1.0/a-1.0.jar", true},
		{"maven", "g/a/1.0/a-1.0.jar.sha1", true},
		{"maven", "g/a/maven-metadata.xml", false},
		{"maven", "g/a/maven-metadata.xml.sha1", false},
		{"maven", "g/a/1.0-SNAPSHOT/a-1.0-20240101.000000-1.jar", false},
		{"maven", "g/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.pom", false},
		{"maven", "g/a/archetype-catalog.xml", false},
		{"maven", "g/a/1.0/a-1.0.jar.asc", true},
		{"maven", "g/a/maven-metadata.xml.asc", false},
		{"deb", "pool/main/f/foo/foo_1.0_amd64.deb", true},
		{"deb", "flat/foo_1.0_amd64.deb", false},
		{"deb", "dists/stable/Release", false},
		{"rpm", "Packages/foo-1.0.x86_64.rpm", true},
		{"rpm", "x86_64/foo-1.0.x86_64.rpm", false},
		{"rpm", "repodata/repomd.xml", false},
		{"raw", "anything.bin", false},
	}

	for _, c := range cases {
		if got := Immutable(c.format, c.key); got != c.want {
			t.Errorf("Immutable(%q, %q) = %v, want %v", c.format, c.key, got, c.want)
		}
	}
}

// waiting reports how many callers are blocked on the in-flight fetch of key.
func (g *flightGroup) waiting(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	if f, ok := g.calls[key]; ok {
		return f.waiters
	}

	return 0
}
