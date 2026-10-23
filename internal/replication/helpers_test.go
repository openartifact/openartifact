// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
)

// memQueue is an in-memory database.BlobReplicationStore with the semantics of
// the SQL one (one row per blob, latest op wins, claim leases, versions).
type memQueue struct {
	mu   sync.Mutex
	now  func() time.Time
	rows map[string]*memRow
	// referenced are the blobs the "database" needs; dbBlobs the blobs table.
	referenced map[string]bool
	dbBlobs    map[string]bool
	state      database.BlobReplicationState
	failEnq    atomic.Bool
}

type memRow struct {
	op       string
	version  int64
	attempts int
	next     time.Time
	claimed  time.Time
	lastErr  string
	enqueued time.Time
}

func newMemQueue(now func() time.Time) *memQueue {
	return &memQueue{now: now, rows: map[string]*memRow{}, referenced: map[string]bool{}, dbBlobs: map[string]bool{}}
}

var errQueueDown = errors.New("queue down")

func (q *memQueue) EnqueueBlobReplication(_ context.Context, sha, op string) error {
	if q.failEnq.Load() {
		return errQueueDown
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if r, ok := q.rows[sha]; ok {
		if r.op == op {
			return nil
		}

		*r = memRow{op: op, version: r.version + 1, next: q.now(), enqueued: q.now()}

		return nil
	}

	q.rows[sha] = &memRow{op: op, version: 1, next: q.now(), enqueued: q.now()}

	return nil
}

func (q *memQueue) ClaimBlobReplications(_ context.Context, asOf time.Time, limit int, claimFor time.Duration) ([]database.BlobReplication, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var due []string

	for sha, r := range q.rows {
		if !r.next.After(asOf) && !r.claimed.After(asOf) {
			due = append(due, sha)
		}
	}

	sort.Slice(due, func(i, j int) bool {
		a, b := q.rows[due[i]], q.rows[due[j]]
		if !a.next.Equal(b.next) {
			return a.next.Before(b.next)
		}

		return due[i] < due[j]
	})

	if len(due) > limit {
		due = due[:limit]
	}

	out := make([]database.BlobReplication, 0, len(due))

	for _, sha := range due {
		r := q.rows[sha]
		r.claimed = asOf.Add(claimFor)
		out = append(out, database.BlobReplication{SHA256: sha, Op: r.op, Version: r.version, Attempts: r.attempts, EnqueuedAt: r.enqueued})
	}

	return out, nil
}

func (q *memQueue) CompleteBlobReplication(_ context.Context, sha string, version int64) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if r, ok := q.rows[sha]; ok && r.version == version {
		delete(q.rows, sha)

		return true, nil
	}

	return false, nil
}

func (q *memQueue) FailBlobReplication(_ context.Context, sha string, version int64, msg string, next time.Time) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	r, ok := q.rows[sha]
	if !ok || r.version != version {
		return false, nil
	}

	r.attempts++
	r.lastErr, r.next, r.claimed = msg, next, time.Time{}

	return true, nil
}

func (q *memQueue) ReleaseBlobReplication(_ context.Context, sha string, version int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if r, ok := q.rows[sha]; ok && r.version == version {
		r.claimed = time.Time{}
	}

	return nil
}

func (q *memQueue) GetBlobReplication(_ context.Context, sha string) (*database.BlobReplication, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	r, ok := q.rows[sha]
	if !ok {
		return nil, nil
	}

	return &database.BlobReplication{SHA256: sha, Op: r.op, Version: r.version, Attempts: r.attempts, EnqueuedAt: r.enqueued, NextAttemptAt: r.next, LastError: r.lastErr}, nil
}

func (q *memQueue) BlobReplicationBacklog(_ context.Context, failing int) (database.BlobReplicationBacklog, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var b database.BlobReplicationBacklog

	for _, r := range q.rows {
		if r.op == database.BlobReplicationCopy {
			b.Copy++
		} else {
			b.Delete++
		}

		if r.attempts >= failing {
			b.Failing++
		}

		b.OldestAgeSeconds = max(b.OldestAgeSeconds, q.now().Sub(r.enqueued).Seconds())
	}

	return b, nil
}

func (q *memQueue) ListBlobSHAs(_ context.Context, after string, limit int) ([]string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var ids []string

	for id := range q.dbBlobs {
		if id > after {
			ids = append(ids, id)
		}
	}

	slices.Sort(ids)

	if len(ids) > limit {
		ids = ids[:limit]
	}

	return ids, nil
}

func (q *memQueue) BlobReplicationReferenced(_ context.Context, sha string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.referenced[sha], nil
}

func (q *memQueue) GetBlobReplicationState(context.Context) (database.BlobReplicationState, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.state, nil
}

func (q *memQueue) AddBlobReplicationProgress(_ context.Context, copies, bytes int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.state.ReplicatedTotal += copies
	q.state.ReplicatedBytes += bytes

	return nil
}

func (q *memQueue) SetBlobReplicationLastError(_ context.Context, msg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.state.LastError = msg
	now := q.now()
	q.state.LastErrorAt = &now

	return nil
}

func (q *memQueue) RequestReplicationVerify(context.Context) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.state.VerifyRun != nil || q.state.VerifyRequested {
		return false, nil
	}

	q.state.VerifyRequested = true

	return true, nil
}

func (q *memQueue) StartReplicationVerify(_ context.Context, run json.RawMessage) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.state.VerifyRun != nil {
		return false, nil
	}

	q.state.VerifyRequested, q.state.VerifyCursor, q.state.VerifyRun = false, "", run

	return true, nil
}

func (q *memQueue) SaveReplicationVerifyProgress(_ context.Context, cursor string, run json.RawMessage) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.state.VerifyRun == nil {
		return false, nil
	}

	q.state.VerifyCursor, q.state.VerifyRun = cursor, run

	return true, nil
}

func (q *memQueue) FinishReplicationVerify(_ context.Context, result json.RawMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.state.LastVerify, q.state.VerifyRun, q.state.VerifyCursor, q.state.VerifyRequested = result, nil, "", false

	return nil
}

func (q *memQueue) op(sha string) *database.BlobReplication {
	r, _ := q.GetBlobReplication(context.Background(), sha)

	return r
}

func (q *memQueue) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return len(q.rows)
}

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.t = c.t.Add(d)
}

// faultStore wraps a store and fails calls on demand.
type faultStore struct {
	storage.BlobStore

	down    atomic.Bool // every call fails
	failPut atomic.Bool
	noList  atomic.Bool
	puts    atomic.Int64
	getErr  atomic.Pointer[error]
	dir     string
}

// file is where the filesystem store keeps the blob.
func (f *faultStore) file(id string) string { return filepath.Join(f.dir, id[:2], id[2:]) }

var errReplicaDown = errors.New("replica down")

func (f *faultStore) fail() error {
	if f.down.Load() {
		return errReplicaDown
	}

	return nil
}

func (f *faultStore) Put(ctx context.Context, r io.Reader, md map[string]string) (string, error) {
	if err := f.fail(); err != nil {
		return "", err
	}

	if f.failPut.Load() {
		return "", errReplicaDown
	}

	f.puts.Add(1)

	return f.BlobStore.Put(ctx, r, md)
}

func (f *faultStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := f.fail(); err != nil {
		return nil, err
	}

	if p := f.getErr.Load(); p != nil {
		return nil, *p
	}

	return f.BlobStore.Get(ctx, id)
}

func (f *faultStore) Exists(ctx context.Context, id string) (bool, error) {
	if err := f.fail(); err != nil {
		return false, err
	}

	if p := f.getErr.Load(); p != nil {
		return false, *p
	}

	return f.BlobStore.Exists(ctx, id)
}

func (f *faultStore) GetSize(ctx context.Context, id string) (int64, error) {
	if err := f.fail(); err != nil {
		return 0, err
	}

	if p := f.getErr.Load(); p != nil {
		return 0, *p
	}

	return f.BlobStore.GetSize(ctx, id)
}

func (f *faultStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	if err := f.fail(); err != nil {
		return nil, err
	}

	if p := f.getErr.Load(); p != nil {
		return nil, *p
	}

	return f.BlobStore.GetMetadata(ctx, id)
}

func (f *faultStore) ExistsBySHA256(ctx context.Context, sha string) (string, bool, error) {
	if err := f.fail(); err != nil {
		return "", false, err
	}

	if p := f.getErr.Load(); p != nil {
		return "", false, *p
	}

	return f.BlobStore.ExistsBySHA256(ctx, sha)
}

func (f *faultStore) Delete(ctx context.Context, id string) error {
	if err := f.fail(); err != nil {
		return err
	}

	return f.BlobStore.Delete(ctx, id)
}

func (f *faultStore) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	if f.noList.Load() {
		return nil, "", storage.ErrListNotSupported
	}

	return f.BlobStore.List(ctx, token, limit)
}

// rig is a replication setup over two filesystem stores and a fake clock.
type rig struct {
	t       *testing.T
	clock   *fakeClock
	primary *faultStore
	replica *faultStore
	queue   *memQueue
	svc     *Service
	store   *Store
	m       *metrics.ReplicationMetrics
}

func newFS(t *testing.T) *faultStore {
	t.Helper()

	dir := t.TempDir()

	fs, err := storage.NewFilesystemBlobStore(storage.StorageConfig{Type: "filesystem", Path: dir})
	if err != nil {
		t.Fatalf("filesystem store: %v", err)
	}

	return &faultStore{BlobStore: fs, dir: dir}
}

func defaultTestOptions() Options {
	return Options{
		ReadFallback: Fallback(storage.ReadFallbackNotFound), Interval: time.Second, Batch: 50, Concurrency: 4,
		VerifyInterval: 0, MaxBackoff: time.Hour,
	}
}

func newRig(t *testing.T, mutate func(*Options)) *rig {
	t.Helper()

	opts := defaultTestOptions()
	if mutate != nil {
		mutate(&opts)
	}

	clock := newFakeClock()
	r := &rig{
		t: t, clock: clock, primary: newFS(t), replica: newFS(t), queue: newMemQueue(clock.Now),
		m: metrics.NewReplicationMetrics(),
	}

	svc, err := New(Config{
		Primary: r.primary, Replica: r.replica, Queue: r.queue, Options: opts, Metrics: r.m,
		Info: ReplicaInfo{Type: "filesystem", Location: "replica"},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	svc.core.now = clock.Now
	svc.core.jitter = func(d time.Duration) time.Duration { return d }
	svc.verifier.budget = time.Hour
	r.svc, r.store = svc, svc.Store()

	t.Cleanup(svc.Stop)

	return r
}

func sum(data []byte) string {
	h := sha256.Sum256(data)

	return hex.EncodeToString(h[:])
}

// put writes data through the replicating store and returns its id.
func (r *rig) put(data string) string {
	r.t.Helper()

	id, err := r.store.Put(context.Background(), bytes.NewReader([]byte(data)), nil)
	if err != nil {
		r.t.Fatalf("put: %v", err)
	}

	if id != sum([]byte(data)) {
		r.t.Fatalf("put id %s", id)
	}

	return id
}

// drain runs the replicator until nothing is due.
func (r *rig) drain() {
	r.t.Helper()

	for range 100 {
		n, err := r.svc.replicator.RunOnce(context.Background())
		if err != nil {
			r.t.Fatalf("run once: %v", err)
		}

		if n == 0 {
			return
		}
	}

	r.t.Fatal("queue did not drain")
}

func readAll(t *testing.T, s storage.BlobStore, id string) string {
	t.Helper()

	rc, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}

	defer func() { _ = rc.Close() }()

	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}

	return string(b)
}

func has(t *testing.T, s storage.BlobStore, id string) bool {
	t.Helper()

	ok, err := s.Exists(context.Background(), id)
	if err != nil {
		t.Fatalf("exists %s: %v", id, err)
	}

	return ok
}

// damage overwrites the blob's bytes in a filesystem store.
func damage(t *testing.T, s *faultStore, id, content string) {
	t.Helper()

	if err := os.WriteFile(s.file(id), []byte(content), 0o600); err != nil {
		t.Fatalf("damage %s: %v", id, err)
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// twoTier is a primary spread over several stores, like the tiered store: it
// finds a blob on any tier and lists them separately (TierStores).
type twoTier struct {
	storage.BlobStore

	tiers []*faultStore
}

func (s *twoTier) TierStores() []storage.BlobStore {
	out := make([]storage.BlobStore, 0, len(s.tiers))
	for _, t := range s.tiers {
		out = append(out, t)
	}

	return out
}

func (s *twoTier) find(ctx context.Context, id string) *faultStore {
	for _, t := range s.tiers {
		if ok, _ := t.Exists(ctx, id); ok {
			return t
		}
	}

	return nil
}

func (s *twoTier) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if t := s.find(ctx, id); t != nil {
		return t.Get(ctx, id)
	}

	return nil, storage.ErrBlobNotFound
}

func (s *twoTier) GetSize(ctx context.Context, id string) (int64, error) {
	if t := s.find(ctx, id); t != nil {
		return t.GetSize(ctx, id)
	}

	return 0, storage.ErrBlobNotFound
}

func (s *twoTier) Exists(ctx context.Context, id string) (bool, error) {
	return s.find(ctx, id) != nil, nil
}
