// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const sweepBackend = "11111111-1111-1111-1111-111111111111"

// queueDB is an in-memory deletion queue with the claim, hold and per-blob lock
// semantics the sweeper relies on.
type queueDB struct {
	database.BaseMockDB

	mu      sync.Mutex
	rows    map[string]*database.BlobDeletion // "sha|scope"
	claimed map[string]bool
	held    map[string]bool   // deletions a running backup holds back
	blobs   map[string]string // sha -> placement backend, for sha with a blobs row
	refs    map[string]int

	lockMu sync.Mutex
	locks  map[string]*sync.Mutex
	locked map[string]bool
}

func newQueueDB() *queueDB {
	return &queueDB{
		rows: map[string]*database.BlobDeletion{}, claimed: map[string]bool{}, held: map[string]bool{},
		blobs: map[string]string{}, refs: map[string]int{},
		locks: map[string]*sync.Mutex{}, locked: map[string]bool{},
	}
}

func key(sha, scope string) string { return sha + "|" + scope }

func (d *queueDB) enqueue(sha, scope string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.rows[key(sha, scope)] = &database.BlobDeletion{SHA256: sha, Scope: scope}
}

func (d *queueDB) ClaimBlobDeletions(_ context.Context, limit int, _ time.Duration) ([]database.BlobDeletion, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []database.BlobDeletion

	for k, r := range d.rows {
		if len(out) == limit {
			break
		}

		if d.claimed[k] || d.held[k] {
			continue
		}

		d.claimed[k] = true
		r.Attempts++
		out = append(out, *r)
	}

	return out, nil
}

func (d *queueDB) BlobDeletionDue(_ context.Context, sha, scope string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, ok := d.rows[key(sha, scope)]

	return ok && !d.held[key(sha, scope)], nil
}

func (d *queueDB) DeleteBlobDeletion(_ context.Context, sha, scope string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.rows, key(sha, scope))
	delete(d.claimed, key(sha, scope))

	return nil
}

func (d *queueDB) CountBlobDeletions(context.Context) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return int64(len(d.rows)), nil
}

func (d *queueDB) PurgeExpiredBackupLeases(context.Context) (int64, error) { return 0, nil }

func (d *queueDB) GetBlobPlacement(_ context.Context, sha string) (string, int64, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	backend, ok := d.blobs[sha]

	return backend, 1, ok, nil
}

func (d *queueDB) BlobReferenceCount(_ context.Context, sha string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.refs[sha], nil
}

func (d *queueDB) WithBlobLock(ctx context.Context, sha string, fn func(ctx context.Context) error) error {
	d.lockMu.Lock()

	l, ok := d.locks[sha]
	if !ok {
		l = &sync.Mutex{}
		d.locks[sha] = l
	}

	d.lockMu.Unlock()

	l.Lock()
	defer l.Unlock()

	d.setLocked(sha, true)
	defer d.setLocked(sha, false)

	return fn(ctx)
}

func (d *queueDB) setLocked(sha string, v bool) {
	d.lockMu.Lock()
	d.locked[sha] = v
	d.lockMu.Unlock()
}

func (d *queueDB) isLocked(sha string) bool {
	d.lockMu.Lock()
	defer d.lockMu.Unlock()

	return d.locked[sha]
}

// releaseClaims stands for the claim period running out.
func (d *queueDB) releaseClaims() {
	d.mu.Lock()
	d.claimed = map[string]bool{}
	d.mu.Unlock()
}

// byteStore records deletions and checks they happen under the blob lock.
type byteStore struct {
	db *queueDB

	mu       sync.Mutex
	deleted  []string // "sha" or "sha@backend"
	unlocked []string // deletions that ran without the blob lock
	err      error
}

func (s *byteStore) record(entry, sha string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return s.err
	}

	if !s.db.isLocked(sha) {
		s.unlocked = append(s.unlocked, entry)
	}

	s.deleted = append(s.deleted, entry)

	return nil
}

func (s *byteStore) Delete(_ context.Context, sha string) error { return s.record(sha, sha) }

func (s *byteStore) DeleteCopy(_ context.Context, backend, sha string) error {
	return s.record(sha+"@"+backend, sha)
}

func (s *byteStore) deletions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string{}, s.deleted...)
}

func newSweeper(t *testing.T, db *queueDB, store *byteStore) *BlobDeletionSweeper {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return NewBlobDeletionSweeper(db, store, log, 0, 10)
}

func sha(c string) string { return strings.Repeat(c, 64) }

func mustRun(t *testing.T, w *BlobDeletionSweeper) BlobDeletionStats {
	t.Helper()

	st, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	return st
}

func TestSweeperDeletesDueBytesUnderTheBlobLock(t *testing.T) {
	t.Parallel()

	db := newQueueDB()
	store := &byteStore{db: db}
	db.enqueue(sha("a"), database.BlobDeletionScopeAll)

	st := mustRun(t, newSweeper(t, db, store))

	if st.Deleted != 1 || st.Pending != 0 || len(store.deletions()) != 1 || store.deletions()[0] != sha("a") {
		t.Fatalf("stats %+v deletions %v, want the one blob deleted and the queue empty", st, store.deletions())
	}

	if len(store.unlocked) != 0 {
		t.Errorf("bytes removed without the blob lock: %v", store.unlocked)
	}
}

func TestSweeperLeavesDeletionsHeldByABackup(t *testing.T) {
	t.Parallel()

	db := newQueueDB()
	store := &byteStore{db: db}
	db.enqueue(sha("a"), database.BlobDeletionScopeAll)
	db.held[key(sha("a"), database.BlobDeletionScopeAll)] = true

	st := mustRun(t, newSweeper(t, db, store))

	if st.Deleted != 0 || st.Pending != 1 || len(store.deletions()) != 0 {
		t.Fatalf("stats %+v deletions %v, want nothing touched while a backup holds the blob", st, store.deletions())
	}

	// The backup ends; the next pass takes it.
	db.mu.Lock()
	delete(db.held, key(sha("a"), database.BlobDeletionScopeAll))
	db.mu.Unlock()

	if st := mustRun(t, newSweeper(t, db, store)); st.Deleted != 1 {
		t.Fatalf("after the backup ended: %+v, want the blob deleted", st)
	}
}

// A blob uploaded again during the grace period has a blobs row (or is
// referenced by generated metadata) by the time the sweeper looks: keep its
// bytes and drop the stale entry.
func TestSweeperKeepsBytesOfReuploadedAndReferencedBlobs(t *testing.T) {
	t.Parallel()

	db := newQueueDB()
	store := &byteStore{db: db}

	db.enqueue(sha("a"), database.BlobDeletionScopeAll)
	db.blobs[sha("a")] = "" // re-uploaded: a blobs row exists again
	db.enqueue(sha("b"), database.BlobDeletionScopeAll)
	db.refs[sha("b")] = 1 // metadata points at the bytes, no blobs row

	st := mustRun(t, newSweeper(t, db, store))

	if st.Kept != 2 || st.Deleted != 0 || st.Pending != 0 || len(store.deletions()) != 0 {
		t.Fatalf("stats %+v deletions %v, want both kept and dropped from the queue", st, store.deletions())
	}
}

func TestSweeperRetiresOnlyTheQueuedCopy(t *testing.T) {
	t.Parallel()

	db := newQueueDB()
	store := &byteStore{db: db}

	// The blob moved to the cold backend: the primary copy is retired.
	db.blobs[sha("a")] = sweepBackend
	db.enqueue(sha("a"), database.BlobDeletionScopePrimary)

	// The blob moved back: the "old" cold copy is authoritative again.
	db.blobs[sha("b")] = sweepBackend
	db.enqueue(sha("b"), sweepBackend)

	st := mustRun(t, newSweeper(t, db, store))

	want := sha("a") + "@"
	if st.Deleted != 1 || st.Kept != 1 || len(store.deletions()) != 1 || store.deletions()[0] != want {
		t.Fatalf("stats %+v deletions %v, want only %q removed", st, store.deletions(), want)
	}
}

func TestSweeperRetriesFailuresAndTreatsMissingBytesAsDone(t *testing.T) {
	t.Parallel()

	db := newQueueDB()
	store := &byteStore{db: db, err: errors.New("backend unavailable")}
	db.enqueue(sha("a"), database.BlobDeletionScopeAll)

	w := newSweeper(t, db, store)

	if st := mustRun(t, w); st.Failed != 1 || st.Pending != 1 {
		t.Fatalf("failing store: %+v, want 1 failed and still queued", st)
	}

	// Still claimed: the claim period is the retry delay.
	if st := mustRun(t, w); st.Failed != 0 || st.Deleted != 0 || st.Pending != 1 {
		t.Fatalf("immediately after: %+v, want the claimed entry left alone", st)
	}

	db.releaseClaims()

	store.mu.Lock()
	store.err = fmt.Errorf("gone: %w", storage.ErrBlobNotFound)
	store.mu.Unlock()

	if st := mustRun(t, w); st.Deleted != 1 || st.Pending != 0 {
		t.Fatalf("object already gone: %+v, want counted as deleted", st)
	}
}

func TestSweeperPublishesMetrics(t *testing.T) {
	t.Parallel()

	db := newQueueDB()
	store := &byteStore{db: db}
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	db.enqueue(sha("a"), database.BlobDeletionScopeAll)
	db.enqueue(sha("b"), database.BlobDeletionScopeAll)
	db.blobs[sha("b")] = ""
	db.enqueue(sha("c"), database.BlobDeletionScopeAll)
	db.held[key(sha("c"), database.BlobDeletionScopeAll)] = true

	mustRun(t, newSweeper(t, db, store).WithMetrics(reg))

	if got := testutil.ToFloat64(reg.BlobDeletionsPending); got != 1 {
		t.Errorf("pending gauge = %v, want 1 (the held one)", got)
	}

	if got := testutil.ToFloat64(reg.BlobDeletionsSwept.WithLabelValues("deleted")); got != 1 {
		t.Errorf("swept{deleted} = %v, want 1", got)
	}

	if got := testutil.ToFloat64(reg.BlobDeletionsSwept.WithLabelValues("kept")); got != 1 {
		t.Errorf("swept{kept} = %v, want 1", got)
	}
}

// Two sweepers (two replicas) draining one queue delete every blob exactly once.
func TestConcurrentSweepersDoNotDoubleProcess(t *testing.T) {
	t.Parallel()

	const n = 60

	db := newQueueDB()
	store := &byteStore{db: db}

	for i := range n {
		db.enqueue(fmt.Sprintf("%064d", i), database.BlobDeletionScopeAll)
	}

	var (
		wg    sync.WaitGroup
		total [2]int
	)

	for i := range 2 {
		wg.Go(func() {
			st, err := newSweeper(t, db, store).RunOnce(context.Background())
			if err != nil {
				t.Error(err)
			}

			total[i] = st.Deleted
		})
	}

	wg.Wait()

	seen := map[string]int{}
	for _, d := range store.deletions() {
		seen[d]++
	}

	if len(seen) != n || total[0]+total[1] != n {
		t.Fatalf("deleted %d distinct blobs, sweepers report %v, want %d", len(seen), total, n)
	}

	for s, c := range seen {
		if c != 1 {
			t.Errorf("%s deleted %d times", s, c)
		}
	}
}

func TestSweeperScheduleProcessesTheQueueAndStopsCleanly(t *testing.T) {
	t.Parallel()

	db := newQueueDB()
	store := &byteStore{db: db}
	db.enqueue(sha("a"), database.BlobDeletionScopeAll)

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	w := NewBlobDeletionSweeper(db, store, log, 5*time.Millisecond, 10)
	w.Start()
	w.Start() // only the first call counts

	deadline := time.Now().Add(5 * time.Second)

	for len(store.deletions()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("scheduled pass never deleted the queued blob")
		}

		time.Sleep(time.Millisecond)
	}

	w.Stop()
	w.Stop() // idempotent
}

func TestSweeperWithoutQueueDoesNothing(t *testing.T) {
	t.Parallel()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	w := NewBlobDeletionSweeper(&database.BaseMockDB{}, &byteStore{db: newQueueDB()}, log, time.Millisecond, 10)
	w.Start()
	w.Stop()

	if st, err := w.RunOnce(context.Background()); err != nil || st != (BlobDeletionStats{}) {
		t.Fatalf("RunOnce = %+v, %v; want a no-op", st, err)
	}
}
