// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
	pkgdelta "github.com/openartifact/openartifact/pkg/delta"
	"github.com/openartifact/openartifact/pkg/logger"
)

type deltaTestDB struct {
	DeltaStore

	comps       []models.Component
	quarantined map[string]bool

	mu       sync.Mutex
	upserted []string
}

func (d *deltaTestDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return &models.Repository{ID: "repo1", Name: name}, nil
}

func (d *deltaTestDB) GetComponentByID(_ context.Context, id string) (*models.Component, error) {
	for i := range d.comps {
		if d.comps[i].ID == id {
			return &d.comps[i], nil
		}
	}

	return nil, nil
}

func (d *deltaTestDB) GetComponentBySHA256(_ context.Context, repoID, sha string) (*models.Component, error) {
	for i := range d.comps {
		if d.comps[i].RepositoryID == repoID && d.comps[i].SHA256 == sha {
			return &d.comps[i], nil
		}
	}

	return nil, nil
}

func (d *deltaTestDB) GetPackageDelta(context.Context, string, string, string) (*models.PackageDelta, error) {
	return nil, nil
}

func (d *deltaTestDB) CreatePackageDelta(context.Context, *models.PackageDelta) error { return nil }

func (d *deltaTestDB) UpsertBlob(_ context.Context, sha, _ string, _ int64) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.upserted = append(d.upserted, sha)

	return "blob-" + sha, nil
}

func (d *deltaTestDB) upsertCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return len(d.upserted)
}

// IsArtifactQuarantined makes deltaTestDB a quarantine checker.
func (d *deltaTestDB) IsArtifactQuarantined(_ context.Context, _ string, keys []string) (bool, error) {
	for _, k := range keys {
		if d.quarantined[k] {
			return true, nil
		}
	}

	return false, nil
}

func TestDeltaFromValidationAndScoping(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	oldID, _ := store.Put(ctx, bytes.NewReader([]byte("old package content")), nil)
	newID, _ := store.Put(ctx, bytes.NewReader([]byte("new package content!")), nil)

	db := &deltaTestDB{comps: []models.Component{
		{ID: "to", RepositoryID: "repo1", SHA256: newID, Filename: "new.pkg"},
		{ID: "from", RepositoryID: "repo1", SHA256: oldID, Filename: "old.pkg"},
		{ID: "foreign", RepositoryID: "other", SHA256: newID, Filename: "x.pkg"},
	}}

	router := chi.NewRouter()
	NewDeltaHandler(log, db, store).Register(router)

	get := func(path string) int {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody))

		return rec.Code
	}

	base := "/api/v1/repositories/r/packages/"

	if c := get(base + "to/delta?from=../../../../etc/passwd"); c != http.StatusBadRequest {
		t.Errorf("traversal from: got %d, want 400", c)
	}

	unknown := "00000000000000000000000000000000000000000000000000000000000000aa"
	if c := get(base + "to/delta?from=" + unknown); c != http.StatusNotFound {
		t.Errorf("from not in repo: got %d, want 404", c)
	}

	if c := get(base + "foreign/delta?from=" + oldID); c != http.StatusNotFound {
		t.Errorf("foreign-repo target: got %d, want 404", c)
	}

	if c := get(base + "to/delta?from=" + oldID); c != http.StatusOK {
		t.Errorf("valid delta: got %d, want 200", c)
	}

	// API-1: the endpoint is RBAC-gated, so a delta response must never be
	// marked public for a request that is not an anonymous public read.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, base+"to/delta?from="+oldID, http.NoBody))

	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "public") {
		t.Errorf("Cache-Control = %q, must not be public", cc)
	}
}

func newDeltaFixture(t *testing.T) (*chi.Mux, *deltaTestDB, *DeltaHandler, string) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	oldID, _ := store.Put(context.Background(), bytes.NewReader([]byte("old package content")), nil)
	newID, _ := store.Put(context.Background(), bytes.NewReader([]byte("new package content!")), nil)

	db := &deltaTestDB{comps: []models.Component{
		{ID: "to", RepositoryID: "repo1", SHA256: newID, Filename: "new.pkg"},
		{ID: "from", RepositoryID: "repo1", SHA256: oldID, Filename: "old.pkg"},
	}, quarantined: map[string]bool{}}

	h := NewDeltaHandler(log, db, store)
	router := chi.NewRouter()
	h.Register(router)

	return router, db, h, oldID
}

func TestDeltaDeniesQuarantinedSides(t *testing.T) {
	for _, key := range []string{"new.pkg", "old.pkg"} {
		router, db, _, oldID := newDeltaFixture(t)
		db.quarantined[key] = true

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet,
			"/api/v1/repositories/r/packages/to/delta?from="+oldID, http.NoBody))

		if rec.Code != http.StatusForbidden {
			t.Errorf("quarantined %s: got %d, want 403", key, rec.Code)
		}

		if db.upsertCount() != 0 {
			t.Errorf("quarantined %s: a patch was generated", key)
		}
	}
}

func TestDeltaGenerationRegistersBlob(t *testing.T) {
	router, db, _, oldID := newDeltaFixture(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v1/repositories/r/packages/to/delta?from="+oldID, http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	if db.upsertCount() != 1 {
		t.Errorf("patch blob registrations = %d, want 1 (so GC and refcounts see it)", db.upsertCount())
	}
}

func TestDeltaGenerationIsBounded(t *testing.T) {
	_, db, h, oldID := newDeltaFixture(t)

	for range maxConcurrentDeltas {
		h.slots <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := h.generateDelta(ctx, "repo1", oldID, "old.pkg", &db.comps[0], pkgdelta.MaxInputSize); err == nil {
		t.Fatal("generation ran although every slot was taken")
	}
}

// newDeltaSourcesFixture builds a repository with one target package and n
// distinct source packages; it returns the sources' sha256.
func newDeltaSourcesFixture(t *testing.T, n int) (*chi.Mux, *deltaTestDB, *DeltaHandler, []string) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	newID, _ := store.Put(ctx, bytes.NewReader([]byte("new package content!")), nil)

	db := &deltaTestDB{
		comps:       []models.Component{{ID: "to", RepositoryID: "repo1", SHA256: newID, Filename: "new.pkg"}},
		quarantined: map[string]bool{},
	}

	shas := make([]string, 0, n)

	for i := range n {
		id, _ := store.Put(ctx, bytes.NewReader([]byte(fmt.Sprintf("old package content %d", i))), nil)
		shas = append(shas, id)
		db.comps = append(db.comps, models.Component{ID: fmt.Sprintf("from%d", i), RepositoryID: "repo1", SHA256: id, Filename: "old.pkg"})
	}

	h := NewDeltaHandler(log, db, store)
	router := chi.NewRouter()
	h.Register(router)

	return router, db, h, shas
}

func deltaRequest(ctx context.Context, router http.Handler, from string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet,
		"/api/v1/repositories/r/packages/to/delta?from="+from, http.NoBody))

	return rec
}

// fillDeltaSlots takes every generation slot, as busy runs would, and returns
// the function that frees them.
func fillDeltaSlots(h *DeltaHandler) func() {
	for range maxConcurrentDeltas {
		h.slots <- struct{}{}
	}

	var once sync.Once

	return func() {
		once.Do(func() {
			for range maxConcurrentDeltas {
				<-h.slots
			}
		})
	}
}

// onlyInflightCall returns the one generation in flight, once there is one.
func onlyInflightCall(h *DeltaHandler) *deltaCall {
	h.inflightMu.Lock()
	defer h.inflightMu.Unlock()

	if len(h.inflight) != 1 {
		return nil
	}

	for _, c := range h.inflight {
		return c
	}

	return nil
}

// TestDeltaQueueRefusesBeyondTheBound pins that generations beyond the busy
// slots and the bounded queue are refused with 503 and a Retry-After instead of
// parking one request each, and that the queued ones still complete.
func TestDeltaQueueRefusesBeyondTheBound(t *testing.T) {
	const extra = 3

	router, _, h, shas := newDeltaSourcesFixture(t, maxQueuedDeltas+extra)
	release := fillDeltaSlots(h)

	t.Cleanup(release)

	results := make(chan *httptest.ResponseRecorder, len(shas))

	for _, sha := range shas {
		go func() { results <- deltaRequest(context.Background(), router, sha) }()
	}

	// Only the requests past the queue answer while the slots stay taken.
	for range extra {
		select {
		case rec := <-results:
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("refused request answered %d, want 503: %s", rec.Code, rec.Body)
			}

			if rec.Header().Get("Retry-After") == "" {
				t.Error("a refused delta request must carry Retry-After")
			}

			if !strings.Contains(rec.Body.String(), "DELTA_BUSY") {
				t.Errorf("body = %s, want the DELTA_BUSY code", rec.Body)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d requests beyond the queue were refused", 0, extra)
		}
	}

	waitUntil(t, "the queue to fill", func() bool { return h.queued.Load() == maxQueuedDeltas })

	release()

	for range maxQueuedDeltas {
		select {
		case rec := <-results:
			if rec.Code != http.StatusOK {
				t.Fatalf("queued request answered %d, want 200: %s", rec.Code, rec.Body)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("queued requests did not complete once the slots were free")
		}
	}
}

// TestDeltaWaitersSurviveTheLeaderGivingUp pins that a coalesced waiter gets the
// delta although the request that started the generation was canceled.
func TestDeltaWaitersSurviveTheLeaderGivingUp(t *testing.T) {
	router, db, h, shas := newDeltaSourcesFixture(t, 1)
	release := fillDeltaSlots(h)

	t.Cleanup(release)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	leader := make(chan *httptest.ResponseRecorder, 1)
	go func() { leader <- deltaRequest(leaderCtx, router, shas[0]) }()

	var call *deltaCall

	waitUntil(t, "the leader to queue for a slot", func() bool {
		call = onlyInflightCall(h)

		return call != nil && h.queued.Load() == 1
	})

	waiter := make(chan *httptest.ResponseRecorder, 1)
	go func() { waiter <- deltaRequest(context.Background(), router, shas[0]) }()

	waitUntil(t, "the second request to wait on the shared run", func() bool { return call.waiters.Load() == 1 })

	cancelLeader()
	release()

	select {
	case rec := <-waiter:
		if rec.Code != http.StatusOK {
			t.Fatalf("waiter answered %d, want 200: the generation must not die with the leader's request: %s", rec.Code, rec.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiter did not complete")
	}

	<-leader

	if n := db.upsertCount(); n != 1 {
		t.Errorf("patch blob registrations = %d, want 1 (one shared generation)", n)
	}
}

// TestDeltaWaitersAreBounded pins that requests waiting for one shared
// generation are refused with 503 beyond maxDeltaWaiters.
func TestDeltaWaitersAreBounded(t *testing.T) {
	router, _, h, shas := newDeltaSourcesFixture(t, 1)
	release := fillDeltaSlots(h)

	t.Cleanup(release)

	leader := make(chan *httptest.ResponseRecorder, 1)
	go func() { leader <- deltaRequest(context.Background(), router, shas[0]) }()

	var call *deltaCall

	waitUntil(t, "the leader to queue for a slot", func() bool {
		call = onlyInflightCall(h)

		return call != nil
	})

	call.waiters.Store(maxDeltaWaiters)

	rec := deltaRequest(context.Background(), router, shas[0])
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("waiter beyond the bound: %d (Retry-After %q), want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}

	call.waiters.Store(0)
	release()

	if rec := <-leader; rec.Code != http.StatusOK {
		t.Fatalf("leader answered %d, want 200: %s", rec.Code, rec.Body)
	}
}

// TestDeltaAnonymousInputIsCapped pins that a delta is not generated for an
// anonymous reader (or a request with no resolved principal) from inputs above
// maxAnonymousDeltaInput, while a signed-in reader still gets it.
func TestDeltaAnonymousInputIsCapped(t *testing.T) {
	router, db, h, shas := newDeltaSourcesFixture(t, 1)

	// 9 MiB: above the 8 MiB anonymous cap, under the 64 MiB a signed-in reader
	// may have generated.
	big := make([]byte, 9<<20)
	if int64(len(big)) <= maxAnonymousDeltaInput || int64(len(big)) > pkgdelta.MaxInputSize {
		t.Fatalf("test input of %d bytes is not between the anonymous cap and the signed-in limit", len(big))
	}

	bigID, err := h.blobStore.Put(context.Background(), bytes.NewReader(big), nil)
	if err != nil {
		t.Fatal(err)
	}

	db.comps[0].SHA256 = bigID

	withPrincipal := func(p *auth.Principal) context.Context {
		return middleware.WithPrincipal(context.Background(), p)
	}

	for name, ctx := range map[string]context.Context{
		"no principal":        context.Background(),
		"anonymous principal": withPrincipal(&auth.Principal{IsAnonymous: true}),
	} {
		rec := deltaRequest(ctx, router, shas[0])
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status %d, want 413: %s", name, rec.Code, rec.Body)
		}
	}

	if n := db.upsertCount(); n != 0 {
		t.Fatalf("a patch was generated for an anonymous reader above the cap (%d registrations)", n)
	}

	rec := deltaRequest(withPrincipal(&auth.Principal{Username: "reader"}), router, shas[0])
	if rec.Code != http.StatusOK {
		t.Fatalf("signed-in reader: status %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestDeltaInputLimit(t *testing.T) {
	anon := httptest.NewRequestWithContext(
		middleware.WithPrincipal(context.Background(), &auth.Principal{IsAnonymous: true}),
		http.MethodGet, "/x", http.NoBody)
	user := httptest.NewRequestWithContext(
		middleware.WithPrincipal(context.Background(), &auth.Principal{Username: "u"}),
		http.MethodGet, "/x", http.NoBody)
	none := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", http.NoBody)

	if got := deltaInputLimit(anon); got != maxAnonymousDeltaInput {
		t.Errorf("anonymous limit = %d, want %d", got, maxAnonymousDeltaInput)
	}

	if got := deltaInputLimit(none); got != maxAnonymousDeltaInput {
		t.Errorf("no-principal limit = %d, want the anonymous cap %d (fail closed)", got, maxAnonymousDeltaInput)
	}

	if got := deltaInputLimit(user); got != pkgdelta.MaxInputSize {
		t.Errorf("signed-in limit = %d, want %d", got, pkgdelta.MaxInputSize)
	}
}
