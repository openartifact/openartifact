// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/storage"
)

// fakeDB is an in-memory tiering.DB with the semantics of the Postgres
// implementation that the protocol relies on: a per-blob lock, an atomic flip
// that re-checks the source placement, and a journal keyed by sha256.
type fakeDB struct {
	mu       sync.Mutex
	tiers    []*models.StorageTier
	backends []*models.StorageBackend
	policies []*models.StoragePolicy
	blobs    map[string]*models.BlobFacts
	refs     map[string][]models.BlobRef
	moves    map[string]*models.StorageTierMove
	touched  map[string]time.Time

	// placementErr, when set, fails GetBlobPlacement (a database outage).
	placementErr error
	// usageCalls counts StorageTierUsage calls (the whole-table aggregate).
	usageCalls int
	// deletions are the copies queued for deferred deletion, "sha@backend".
	deletions []string
	// enqueueErr, when set, fails EnqueueBlobDeletion.
	enqueueErr error

	lockMu sync.Mutex
	locks  map[string]*sync.Mutex
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		blobs:   map[string]*models.BlobFacts{},
		refs:    map[string][]models.BlobRef{},
		moves:   map[string]*models.StorageTierMove{},
		touched: map[string]time.Time{},
		locks:   map[string]*sync.Mutex{},
	}
}

func (f *fakeDB) blobLock(sha string) *sync.Mutex {
	f.lockMu.Lock()
	defer f.lockMu.Unlock()

	l, ok := f.locks[sha]
	if !ok {
		l = &sync.Mutex{}
		f.locks[sha] = l
	}

	return l
}

func (f *fakeDB) ListStorageTiers(context.Context) ([]*models.StorageTier, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.tiers), nil
}

func (f *fakeDB) ListStorageBackends(context.Context) ([]*models.StorageBackend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.backends), nil
}

func (f *fakeDB) ListStoragePolicies(context.Context) ([]*models.StoragePolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.policies), nil
}

func (f *fakeDB) StorageTierUsage(context.Context) ([]models.TierUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.usageCalls++

	byBackend := map[string]*models.TierUsage{}

	for _, b := range f.blobs {
		u, ok := byBackend[b.BackendID]
		if !ok {
			u = &models.TierUsage{BackendID: b.BackendID}
			byBackend[b.BackendID] = u
		}

		u.Blobs++
		u.Bytes += b.SizeBytes
	}

	out := make([]models.TierUsage, 0, len(byBackend))
	for _, u := range byBackend {
		out = append(out, *u)
	}

	return out, nil
}

func (f *fakeDB) GetBlobPlacement(_ context.Context, sha string) (string, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.placementErr != nil {
		return "", 0, false, f.placementErr
	}

	b, ok := f.blobs[sha]
	if !ok {
		return "", 0, false, nil
	}

	return b.BackendID, b.SizeBytes, true, nil
}

func (f *fakeDB) WithBlobLock(ctx context.Context, sha string, fn func(ctx context.Context) error) error {
	l := f.blobLock(sha)
	l.Lock()
	defer l.Unlock()

	return fn(ctx)
}

func (f *fakeDB) EnqueueBlobDeletion(_ context.Context, sha, backendID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.enqueueErr != nil {
		return f.enqueueErr
	}

	f.deletions = append(f.deletions, sha+"@"+backendID)

	return nil
}

// queued reports whether the copy of sha on backendID is queued for deletion.
func (f *fakeDB) queued(sha, backendID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Contains(f.deletions, sha+"@"+backendID)
}

func (f *fakeDB) FlipBlobPlacement(ctx context.Context, sha, from, to string, verify func(ctx context.Context) error) (models.FlipOutcome, error) {
	l := f.blobLock(sha)
	l.Lock()
	defer l.Unlock()

	if verify != nil {
		if err := verify(ctx); err != nil {
			return 0, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.blobs[sha]

	switch {
	case !ok:
		return models.FlipBlobGone, nil
	case b.BackendID != from:
		return models.FlipPlacementChanged, nil
	}

	now := time.Now()
	b.BackendID, b.TierChangedAt = to, &now

	if m, ok := f.moves[sha]; ok {
		m.State = models.TierMoveFlipped
	}

	return models.FlipDone, nil
}

func (f *fakeDB) TouchBlobAccess(_ context.Context, accessed map[string]time.Time, _ time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for sha, at := range accessed {
		f.touched[sha] = at

		if b, ok := f.blobs[sha]; ok {
			t := at
			b.LastAccessedAt = &t
		}
	}

	return int64(len(accessed)), nil
}

func (f *fakeDB) BeginStorageTierMove(_ context.Context, m *models.StorageTierMove) (*models.StorageTierMove, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if cur, ok := f.moves[m.SHA256]; ok {
		c := *cur

		return &c, false, nil
	}

	row := *m
	row.State = models.TierMoveCopying
	f.moves[m.SHA256] = &row

	c := row

	return &c, true, nil
}

func (f *fakeDB) ListRecoverableStorageTierMoves(_ context.Context, maxAttempts int, _ time.Duration, limit int) ([]*models.StorageTierMove, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []*models.StorageTierMove

	for _, m := range f.moves {
		if m.Attempts < maxAttempts {
			c := *m
			out = append(out, &c)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].SHA256 < out[j].SHA256 })

	if len(out) > limit {
		out = out[:limit]
	}

	return out, nil
}

func (f *fakeDB) CountStorageTierMoves(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return int64(len(f.moves)), nil
}

func (f *fakeDB) FailStorageTierMove(_ context.Context, sha, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if m, ok := f.moves[sha]; ok {
		m.Attempts++
		m.LastError = message
	}

	return nil
}

func (f *fakeDB) FinishStorageTierMove(_ context.Context, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.moves, sha)

	return nil
}

func clauseMatches(b *models.BlobFacts, c models.TierScanClause) bool {
	idle := b.CreatedAt
	if b.LastAccessedAt != nil {
		idle = *b.LastAccessedAt
	}

	switch {
	case b.BackendID == c.TargetBackendID,
		c.HasFrom && b.BackendID != c.FromBackendID,
		c.CreatedBefore != nil && b.CreatedAt.After(*c.CreatedBefore),
		c.IdleBefore != nil && idle.After(*c.IdleBefore),
		c.MinSizeBytes != nil && b.SizeBytes < *c.MinSizeBytes,
		c.MaxSizeBytes != nil && b.SizeBytes > *c.MaxSizeBytes:
		return false
	}

	return true
}

func (f *fakeDB) sortedShas() []string {
	shas := make([]string, 0, len(f.blobs))
	for sha := range f.blobs {
		shas = append(shas, sha)
	}

	sort.Strings(shas)

	return shas
}

func (f *fakeDB) ScanTierCandidates(_ context.Context, after string, window int, clauses []models.TierScanClause) ([]models.BlobFacts, string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var (
		found   []models.BlobFacts
		scanned int
		last    string
	)

	for _, sha := range f.sortedShas() {
		if sha <= after {
			continue
		}

		if scanned == window {
			return found, last, scanned, nil
		}

		scanned++
		last = sha

		if _, busy := f.moves[sha]; busy {
			continue
		}

		b := f.blobs[sha]
		if slices.ContainsFunc(clauses, func(c models.TierScanClause) bool { return clauseMatches(b, c) }) {
			found = append(found, *b)
		}
	}

	return found, "", scanned, nil
}

func (f *fakeDB) ListPromotionCandidates(_ context.Context, backendIDs []string, after string, limit int) ([]models.BlobFacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []models.BlobFacts

	for _, sha := range f.sortedShas() {
		b := f.blobs[sha]

		if sha <= after || len(out) >= limit || !slices.Contains(backendIDs, b.BackendID) || b.LastAccessedAt == nil {
			continue
		}

		if _, busy := f.moves[sha]; busy {
			continue
		}

		if b.TierChangedAt == nil || b.LastAccessedAt.After(*b.TierChangedAt) {
			out = append(out, *b)
		}
	}

	return out, nil
}

func (f *fakeDB) ListBlobRefs(_ context.Context, shas []string) (map[string][]models.BlobRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := map[string][]models.BlobRef{}

	for _, sha := range shas {
		if r, ok := f.refs[sha]; ok {
			out[sha] = r
		}
	}

	return out, nil
}

// --- fixtures ---------------------------------------------------------------

const (
	testColdBackend = "11111111-1111-1111-1111-111111111111"
	testColdTierID  = "22222222-2222-2222-2222-222222222222"
	testHotTierID   = "33333333-3333-3333-3333-333333333333"
)

// rig is a two-directory tiering setup: hot is the primary filesystem store,
// cold a second filesystem store registered as a tier.
type rig struct {
	t        *testing.T
	db       *fakeDB
	hot      storage.BlobStore
	cold     storage.BlobStore
	hotDir   string
	coldDir  string
	reg      *Registry
	store    *Store
	mover    *Mover
	engine   *Engine
	now      time.Time
	verifyRB bool
}

func newRig(t *testing.T) *rig {
	t.Helper()

	r := &rig{t: t, db: newFakeDB(), now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), verifyRB: true}
	r.hotDir = filepath.Join(t.TempDir(), "hot")
	r.coldDir = filepath.Join(t.TempDir(), "cold")

	var err error

	if r.hot, err = storage.NewBlobStore(storage.StorageConfig{Type: "filesystem", Path: r.hotDir}); err != nil {
		t.Fatalf("hot store: %v", err)
	}

	cfg, _ := json.Marshal(map[string]string{"path": r.coldDir})

	r.db.backends = []*models.StorageBackend{{ID: testColdBackend, Name: "cold", Kind: "filesystem", Config: cfg}}
	r.db.tiers = []*models.StorageTier{
		{ID: testHotTierID, Name: "hot", Rank: 0},
		{ID: testColdTierID, Name: "cold", BackendID: testColdBackend, BackendName: "cold", BackendKind: "filesystem", Rank: 10},
	}

	r.restart()

	snap := r.reg.Snapshot()

	cold, ok := snap.ByBackend(testColdBackend)
	if !ok || cold.Store == nil {
		t.Fatalf("cold tier did not open: %+v", cold)
	}

	r.cold = cold.Store

	return r
}

// restart rebuilds registry, store, mover and engine over the same database and
// directories, the way a process restart does: nothing but the persisted state
// (database rows and stored bytes) survives.
func (r *rig) restart() {
	r.t.Helper()

	r.reg = NewRegistry(r.db, r.hot, storage.StorageConfig{Type: "filesystem", Path: r.hotDir}, nil)
	if err := r.reg.Refresh(context.Background()); err != nil {
		r.t.Fatalf("registry refresh: %v", err)
	}

	r.store = NewStore(r.reg, r.db, nil, nil, nil)
	r.mover = NewMover(r.db, r.reg, r.store, nil, MoverConfig{VerifyReadback: r.verifyRB}, nil, nil)
	r.engine = NewEngine(r.db, r.reg, r.mover, EngineConfig{Batch: 100, Concurrency: 2}, nil, nil)
	r.engine.now = func() time.Time { return r.now }
}

// addBlob stores data on the hot tier, creates its blobs row and returns the sha.
func (r *rig) addBlob(data []byte, age time.Duration) string {
	r.t.Helper()

	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	if _, err := r.store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		r.t.Fatalf("put: %v", err)
	}

	r.db.mu.Lock()
	r.db.blobs[sha] = &models.BlobFacts{SHA256: sha, SizeBytes: int64(len(data)), CreatedAt: r.now.Add(-age)}
	r.db.mu.Unlock()

	return sha
}

// sweep retires the copies the mover queued for deletion, the way the blob
// deletion sweeper does once the grace period has passed and no backup holds the
// bytes: a copy that is authoritative again is kept, any other is removed.
func (r *rig) sweep() {
	r.t.Helper()

	ctx := context.Background()

	r.db.mu.Lock()
	queued := slices.Clone(r.db.deletions)
	r.db.deletions = nil
	r.db.mu.Unlock()

	for _, q := range queued {
		sha, backend, _ := strings.Cut(q, "@")

		if placement, _, found, _ := r.db.GetBlobPlacement(ctx, sha); found && placement == backend {
			continue
		}

		if err := r.store.DeleteCopy(ctx, backend, sha); err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
			r.t.Fatalf("sweep %s: %v", q, err)
		}
	}
}

// where reports which tiers hold the bytes and the recorded placement.
func (r *rig) where(sha string) (bool, bool, string) {
	r.t.Helper()

	ctx := context.Background()
	onHot, _ := r.hot.Exists(ctx, sha)
	onCold, _ := r.cold.Exists(ctx, sha)
	placement, _, _, err := r.db.GetBlobPlacement(ctx, sha)

	if err != nil {
		r.t.Fatalf("GetBlobPlacement: %v", err)
	}

	return onHot, onCold, placement
}

// mustReadable fails unless the blob reads back identical through the tiered store.
func (r *rig) mustReadable(sha string, want []byte) {
	r.t.Helper()

	rc, err := r.store.Get(context.Background(), sha)
	if err != nil {
		r.t.Fatalf("get %s through tiered store: %v", sha[:8], err)
	}
	defer rc.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		r.t.Fatalf("read: %v", err)
	}

	if !bytes.Equal(buf.Bytes(), want) {
		r.t.Fatalf("blob %s read back different bytes", sha[:8])
	}
}

// coldPolicy is "everything older than a day goes to the cold tier".
func (r *rig) coldPolicy() {
	r.db.policies = []*models.StoragePolicy{{
		ID: "p1", Name: "archive", Enabled: true, Position: 1, TargetTierID: testColdTierID, MinAgeSeconds: new(int64(86400)),
	}}
}
