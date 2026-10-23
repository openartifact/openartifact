// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/storage"
)

// lockingDB is a memDB that supports the per-blob lock and records whether a
// lock is currently held, as the postgres implementation pins a connection
// (and a bounded lock slot) for the duration of fn.
type lockingDB struct {
	*memDB

	held  atomic.Bool
	locks atomic.Int32
}

func (d *lockingDB) WithBlobLock(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	d.locks.Add(1)
	d.held.Store(true)

	defer d.held.Store(false)

	return fn(ctx)
}

// lockAwareStore records, per Put and Exists call, whether the blob lock was
// held. With vanish set, the object is deleted right after the first Put, as a
// concurrent SafeDeleteBlob of an unreferenced identical blob would.
type lockAwareStore struct {
	storage.BlobStore

	db     *lockingDB
	vanish bool

	mu         sync.Mutex
	putHeld    []bool
	existsHeld []bool
}

func (s *lockAwareStore) Put(ctx context.Context, data io.Reader, meta map[string]string) (string, error) {
	held := s.db.held.Load()

	id, err := s.BlobStore.Put(ctx, data, meta)

	s.mu.Lock()
	s.putHeld = append(s.putHeld, held)
	first := len(s.putHeld) == 1
	s.mu.Unlock()

	if err == nil && s.vanish && first {
		_ = s.Delete(ctx, id)
	}

	return id, err
}

func (s *lockAwareStore) Exists(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	s.existsHeld = append(s.existsHeld, s.db.held.Load())
	s.mu.Unlock()

	return s.BlobStore.Exists(ctx, id)
}

func newLockedFixture(t *testing.T, handler http.HandlerFunc) (*fixture, *lockAwareStore, *lockingDB) {
	t.Helper()

	f := newFixture(t, handler)
	ldb := &lockingDB{memDB: f.db}
	store := &lockAwareStore{BlobStore: f.svc.blobStore, db: ldb}
	f.svc.db = ldb
	f.svc.blobStore = store

	return f, store, ldb
}

// The upload to the object store must not run inside the blob lock: the lock
// pins a database connection and a bounded slot for as long as it is held, and
// an upload can take minutes.
func TestServe_BlobUploadRunsOutsideTheBlobLock(t *testing.T) {
	f, store, ldb := newLockedFixture(t, okBody("jar-bytes"))

	if w := f.do(http.MethodGet, "g/a/1.0/a-1.0.jar"); w.Code != http.StatusOK || w.Body.String() != "jar-bytes" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	if len(store.putHeld) != 1 || store.putHeld[0] {
		t.Errorf("Put calls under lock = %v, want exactly one, outside the lock", store.putHeld)
	}

	if got := ldb.locks.Load(); got != 1 {
		t.Errorf("blob lock taken %d times, want once around the database work", got)
	}

	// The blob must be confirmed present while the lock excludes deleters.
	if len(store.existsHeld) != 1 || !store.existsHeld[0] {
		t.Errorf("Exists calls under lock = %v, want one, inside the lock", store.existsHeld)
	}

	if f.db.entry("repo-1", "g/a/1.0/a-1.0.jar") == nil || f.db.blobs[sum("jar-bytes")] == 0 {
		t.Error("cache entry or blob row not recorded")
	}
}

// A blob that a concurrent delete removed between the upload and the lock is
// stored again under the lock, so no cache row is committed without its blob.
func TestServe_BlobDeletedBeforeLockIsStoredAgain(t *testing.T) {
	f, store, ldb := newLockedFixture(t, okBody("jar-bytes"))
	store.vanish = true

	inner := store.BlobStore

	if w := f.do(http.MethodGet, "g/a/1.0/a-1.0.jar"); w.Code != http.StatusOK || w.Body.String() != "jar-bytes" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	if len(store.putHeld) != 2 || store.putHeld[0] || !store.putHeld[1] {
		t.Errorf("Put calls under lock = %v, want [false true]: first outside, re-store inside", store.putHeld)
	}

	if ok, err := inner.Exists(t.Context(), sum("jar-bytes")); err != nil || !ok {
		t.Errorf("blob present after caching = %v, %v", ok, err)
	}

	if got := ldb.locks.Load(); got != 1 {
		t.Errorf("blob lock taken %d times, want 1", got)
	}
}

func TestOCI_BlobUploadRunsOutsideTheBlobLock(t *testing.T) {
	f := newOCIFixture(t)
	layer := f.reg.addBlob("library/alpine", "layer-bytes")

	ldb := &lockingDB{memDB: f.db}
	store := &lockAwareStore{BlobStore: f.svc.blobStore, db: ldb}
	f.svc.db = ldb
	f.svc.blobStore = store

	if w := f.do(http.MethodGet, "hub/library/alpine/blobs/"+layer); w.Code != http.StatusOK || w.Body.String() != "layer-bytes" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	if len(store.putHeld) != 1 || store.putHeld[0] {
		t.Errorf("Put calls under lock = %v, want exactly one, outside the lock", store.putHeld)
	}

	if len(f.svc.fetches) != 0 {
		t.Errorf("%d upstream fetch slot(s) still held after the pull", len(f.svc.fetches))
	}
}

// Distinct keys fetch in parallel only up to the gate: with 2 slots and 5
// cold keys the upstream never sees more than 2 transfers at once, and every
// request still succeeds once slots free up.
func TestServe_UpstreamFetchesAreBounded(t *testing.T) {
	const (
		limit   = 2
		clients = 5
	)

	var cur, peak atomic.Int32

	arrived := make(chan struct{}, clients)
	release := make(chan struct{}, clients)

	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		n := cur.Add(1)

		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}

		arrived <- struct{}{}

		<-release

		cur.Add(-1)

		_, _ = io.WriteString(w, "body")
	})
	f.svc.fetches = base.NewGate(limit)

	codes := make(chan int, clients)

	var wg sync.WaitGroup

	for i := range clients {
		wg.Go(func() {
			codes <- f.do(http.MethodGet, fmt.Sprintf("g/a/%d/a-%d.jar", i, i)).Code
		})
	}

	for range limit {
		<-arrived
	}

	// The gate is full: every further fetch is queued before its request.
	if got := len(f.svc.fetches); got != limit {
		t.Fatalf("slots held with %d transfers in flight = %d", limit, got)
	}

	for i := range clients {
		release <- struct{}{}

		if i < clients-limit {
			<-arrived
		}
	}

	wg.Wait()
	close(codes)

	for c := range codes {
		if c != http.StatusOK {
			t.Errorf("status = %d, want 200", c)
		}
	}

	if got := peak.Load(); got > limit {
		t.Errorf("peak concurrent upstream transfers = %d, want <= %d", got, limit)
	}

	if got := f.hits.Load(); got != clients {
		t.Errorf("upstream requests = %d, want %d", got, clients)
	}

	if got := len(f.svc.fetches); got != 0 {
		t.Errorf("%d fetch slot(s) leaked", got)
	}
}

// A failed fetch gives its slot back.
func TestServe_FailedFetchReleasesItsSlot(t *testing.T) {
	f := newFixture(t, http.NotFound)
	f.svc.fetches = base.NewGate(1)
	f.repo.NegativeCacheTTLSeconds = 0

	for range 3 {
		if w := f.do(http.MethodGet, "g/a/9/a-9.jar"); w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	}

	if got := len(f.svc.fetches); got != 0 {
		t.Errorf("%d fetch slot(s) leaked", got)
	}
}

// A queue wait that ends with its context is an upstream failure like an
// unreachable host (stale copy allowed, 502 to the client), and nothing is
// sent upstream.
func TestAcquireFetch_ContextEndedIsUpstreamDown(t *testing.T) {
	f := newFixture(t, okBody("x"))
	f.svc.fetches = base.NewGate(1)

	if err := f.svc.fetches.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := f.svc.acquireFetch(ctx, f.repo, "a.jar"); !errors.Is(err, errUpstreamDown) {
		t.Fatalf("acquireFetch on a full gate with a dead context = %v, want errUpstreamDown", err)
	}

	if got := f.hits.Load(); got != 0 {
		t.Errorf("upstream contacted %d times without a slot", got)
	}
}

func TestFlightGroup_RemovesEntriesOnCompletionAndPanic(t *testing.T) {
	var g flightGroup

	size := func() int {
		g.mu.Lock()
		defer g.mu.Unlock()

		return len(g.calls)
	}

	if _, err := g.do(t.Context(), "done", func() (*resolution, error) { return &resolution{}, nil }); err != nil {
		t.Fatal(err)
	}

	if n := size(); n != 0 {
		t.Errorf("flights after completion = %d, want 0", n)
	}

	func() {
		defer func() { _ = recover() }()

		_, _ = g.do(t.Context(), "boom", func() (*resolution, error) { panic("leader died") })
	}()

	if n := size(); n != 0 {
		t.Errorf("flights after a panicking leader = %d, want 0", n)
	}

	res, err := g.do(t.Context(), "boom", func() (*resolution, error) { return &resolution{fromCache: true}, nil })
	if err != nil || res == nil || !res.fromCache {
		t.Errorf("key reused after a panic: %v, %v", res, err)
	}
}

func TestImmutable_MavenSidecarsFollowTheirFile(t *testing.T) {
	for _, c := range []struct {
		key  string
		want bool
	}{
		{"g/a/1.0/a-1.0.jar", true},
		{"g/a/1.0/a-1.0.jar.md5", true},
		{"g/a/1.0/a-1.0.jar.sha1", true},
		{"g/a/1.0/a-1.0.jar.sha256", true},
		{"g/a/1.0/a-1.0.jar.sha512", true},
		{"g/a/1.0/a-1.0.jar.asc", true},
		{"g/a/1.0/a-1.0.jar.asc.sha1", true},
		{"g/a/archetype-catalog.xml", false},
		{"archetype-catalog.xml", false},
		{"archetype-catalog.xml.sha1", false},
		{"archetype-catalog.xml.asc", false},
		{"g/a/maven-metadata.xml", false},
		{"g/a/maven-metadata.xml.asc", false},
		{"g/a/maven-metadata-central.xml", false},
		{"g/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.jar.sha1", false},
		{"g/a/1.0-SNAPSHOT/a-1.0-20240101.1-1.pom", false},
		{"g/a/1.0/.sha1", false},
	} {
		if got := Immutable("maven", c.key); got != c.want {
			t.Errorf("Immutable(maven, %q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestImmutable_DebAndRPMNeedAnImmutableLayout(t *testing.T) {
	for _, c := range []struct {
		format, key string
		want        bool
	}{
		{"deb", "pool/main/f/foo/foo_1.0_amd64.deb", true},
		{"deb", "pool/main/f/foo/foo_1.0_amd64.udeb", true},
		{"deb", "debian/pool/main/f/foo/foo_1.0_amd64.deb", true},
		{"deb", "foo_1.0_amd64.deb", false},
		{"deb", "flat/foo_1.0_amd64.deb", false},
		{"deb", "pool/main/f/foo/foo_1.0.orig.tar.gz", false},
		{"deb", "dists/stable/main/binary-amd64/Packages.gz", false},
		{"rpm", "Packages/f/foo-1.0-1.x86_64.rpm", true},
		{"rpm", "updates/40/x86_64/Packages/f/foo-1.0-1.x86_64.rpm", true},
		{"rpm", "foo-1.0-1.x86_64.rpm", false},
		{"rpm", "x86_64/foo-1.0-1.x86_64.rpm", false},
		{"rpm", "Packages/repodata.xml", false},
	} {
		if got := Immutable(c.format, c.key); got != c.want {
			t.Errorf("Immutable(%q, %q) = %v, want %v", c.format, c.key, got, c.want)
		}
	}
}

func TestServe_ArchetypeCatalogUsesTheRepositoryTTL(t *testing.T) {
	f := newFixture(t, okBody("<archetype-catalog/>"))

	f.do(http.MethodGet, "archetype-catalog.xml")

	e := f.db.entry("repo-1", "archetype-catalog.xml")
	if e == nil {
		t.Fatal("not cached")
	}

	if time.Until(e.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("archetype-catalog.xml expires in %v, want within the repository TTL", time.Until(e.ExpiresAt))
	}
}

func TestServe_FlatDebUsesTheRepositoryTTLPoolIsPinned(t *testing.T) {
	f := newFixture(t, okBody("deb-bytes"))
	f.repo.Format = "deb"

	f.do(http.MethodGet, "flat/foo_1.0_amd64.deb")
	f.do(http.MethodGet, "pool/main/f/foo/foo_1.0_amd64.deb")

	flat := f.db.entry("repo-1", "flat/foo_1.0_amd64.deb")
	pool := f.db.entry("repo-1", "pool/main/f/foo/foo_1.0_amd64.deb")

	if flat == nil || pool == nil {
		t.Fatalf("not cached: flat=%v pool=%v", flat, pool)
	}

	if time.Until(flat.ExpiresAt) > time.Duration(f.repo.CacheTTLSeconds)*time.Second {
		t.Errorf("flat .deb expires in %v, want within the repository TTL", time.Until(flat.ExpiresAt))
	}

	if time.Until(pool.ExpiresAt) < 50*365*24*time.Hour {
		t.Errorf("pool .deb expires in %v, want effectively never", time.Until(pool.ExpiresAt))
	}
}

// A captive portal or WAF answering 200 text/html for a package path must be
// neither pinned nor cached as the artifact.
func TestServe_HTMLPageForPackagePathIsRefused(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html>please log in</html>")
	})

	for _, key := range []string{"g/a/1.0/a-1.0.jar", "g/a/1.0/a-1.0.pom", "g/a/1.0/a-1.0.zip"} {
		if w := f.do(http.MethodGet, key); w.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502", key, w.Code)
		}

		if f.db.entry("repo-1", key) != nil {
			t.Errorf("%s: HTML page was cached", key)
		}
	}
}

// An upstream may not make the proxy store an oversized index or metadata file,
// while a package file of the same size is allowed.
func TestServe_MetadataFetchesAreCapped(t *testing.T) {
	cases := []struct {
		format, key string
	}{
		{"maven", "g/a/maven-metadata.xml"},
		{"deb", "dists/stable/main/binary-amd64/Packages"},
		{"rpm", "repodata/repomd.xml"},
		{"helm", "index.yaml"},
		{"go", "example.com/m/@v/list"},
	}

	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			f := newFixture(t, okBody("0123456789-too-long"))
			f.repo.Format = tc.format
			f.svc.maxMeta = 10

			if w := f.do(http.MethodGet, tc.key); w.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want 502", w.Code)
			}

			if f.db.entry("repo-1", tc.key) != nil {
				t.Error("oversized metadata was cached")
			}
		})
	}
}

func TestIsMetadataKey(t *testing.T) {
	for _, tc := range []struct {
		format, key string
		want        bool
	}{
		{"deb", "pool/main/a/app/app_1_amd64.deb", false},
		{"deb", "dists/stable/InRelease", true},
		{"rpm", "Packages/a-1.rpm", false},
		{"maven", "g/a/1/a-1.jar", false},
		{"npm", "left-pad", true},
		{"npm", "left-pad/-/left-pad-1.0.0.tgz", false},
		{"pypi", "simple/requests/", true},
		{"pypi", "files/x.whl", false},
		{"cargo", "api/v1/crates/a/1/download", false},
		{"cargo", "config.json", true},
		{"nuget", "v3/package/a/1/a.1.nupkg", false},
	} {
		if got := isMetadataKey(tc.format, tc.key); got != tc.want {
			t.Errorf("isMetadataKey(%s, %s) = %v, want %v", tc.format, tc.key, got, tc.want)
		}
	}
}
