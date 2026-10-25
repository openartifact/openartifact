// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

type purgeQuotaCall struct {
	repoID string
	size   int64
	count  int
}

// trashPurgeDB is a database.TrashStore over an in-memory list of expired
// items. Blob references, orphans and quota calls are recorded or scripted so
// tests can observe what the worker does through storage.SafeDeleteBlob.
type trashPurgeDB struct {
	databasetest.BaseMockDB

	mu sync.Mutex

	expired  []models.TrashRef
	results  map[models.TrashRef]*models.TrashPurgeResult
	failures map[models.TrashRef]error
	attempts map[models.TrashRef]int
	purged   []models.TrashRef
	limits   []int
	// onPurge runs at the start of every PurgeTrash call.
	onPurge func(models.TrashRef)

	refCounts     map[string]int
	orphans       []string
	orphanQueries int
	quota         []purgeQuotaCall
	audits        []*models.AuditLog
}

func newTrashPurgeDB() *trashPurgeDB {
	return &trashPurgeDB{
		results:   map[models.TrashRef]*models.TrashPurgeResult{},
		failures:  map[models.TrashRef]error{},
		attempts:  map[models.TrashRef]int{},
		refCounts: map[string]int{},
	}
}

func (d *trashPurgeDB) ListExpiredTrash(_ context.Context, limit int) ([]models.TrashRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.limits = append(d.limits, limit)

	return append([]models.TrashRef(nil), d.expired[:min(limit, len(d.expired))]...), nil
}

func (d *trashPurgeDB) PurgeTrash(_ context.Context, kind models.TrashKind, id string) (*models.TrashPurgeResult, error) {
	ref := models.TrashRef{Kind: kind, ID: id}

	d.mu.Lock()
	hook := d.onPurge
	d.mu.Unlock()

	if hook != nil {
		hook(ref)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.attempts[ref]++

	if err := d.failures[ref]; err != nil {
		return nil, err
	}

	for i, e := range d.expired {
		if e == ref {
			d.expired = append(d.expired[:i:i], d.expired[i+1:]...)

			break
		}
	}

	d.purged = append(d.purged, ref)

	if res, ok := d.results[ref]; ok {
		return res, nil
	}

	return &models.TrashPurgeResult{Kind: kind, ID: id, RepositoryID: "repo-1", RepositoryName: "files", Format: "raw", Name: id}, nil
}

func (d *trashPurgeDB) BlobReferenceCount(_ context.Context, sha string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.refCounts[sha], nil
}

func (d *trashPurgeDB) ListUnreferencedBlobs(_ context.Context, _ time.Duration, _ string, _, _ int) ([]string, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.orphanQueries++

	shas := d.orphans
	d.orphans = nil

	return shas, "", nil
}

func (d *trashPurgeDB) DecrementStorageUsage(_ context.Context, repoID string, size int64, count int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.quota = append(d.quota, purgeQuotaCall{repoID, size, count})

	return nil
}

func (d *trashPurgeDB) InsertAuditLog(_ context.Context, e *models.AuditLog) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.audits = append(d.audits, e)

	return nil
}

func (d *trashPurgeDB) attemptCount(ref models.TrashRef) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.attempts[ref]
}

func (d *trashPurgeDB) purgedRefs() []models.TrashRef {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]models.TrashRef(nil), d.purged...)
}

// The rest of the TrashStore is not exercised by the worker.
var errTrashPurgeFake = errors.New("trash purge fake: not implemented")

func (d *trashPurgeDB) TrashRows(context.Context, string, models.TrashKind, []string, string) (bool, error) {
	return false, errTrashPurgeFake
}

func (d *trashPurgeDB) TrashRepository(context.Context, string, string) (bool, error) {
	return false, errTrashPurgeFake
}

func (d *trashPurgeDB) ListTrash(context.Context, models.TrashFilter) ([]*models.TrashItem, int, error) {
	return nil, 0, errTrashPurgeFake
}

func (d *trashPurgeDB) GetTrashItem(context.Context, models.TrashKind, string) (*models.TrashItem, error) {
	return nil, errTrashPurgeFake
}

func (d *trashPurgeDB) RestoreTrash(context.Context, models.TrashKind, string) (*models.TrashItem, error) {
	return nil, errTrashPurgeFake
}

func (d *trashPurgeDB) RegenKeys(context.Context, models.TrashKind, string) (string, string, error) {
	return "", "", errTrashPurgeFake
}

func trashSHA(c string) string { return strings.Repeat(c, 64) }

type purgeFixture struct {
	db    *trashPurgeDB
	blobs *formattest.BlobStore
	pub   *formattest.Publisher
	w     *TrashPurgeWorker
}

func newPurgeFixture(t *testing.T, interval time.Duration) *purgeFixture {
	t.Helper()

	// The failure tests log one line per failing item; keep them out of the output.
	log, err := logger.New("error", "json", os.DevNull)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	f := &purgeFixture{db: newTrashPurgeDB(), blobs: formattest.NewBlobStore(), pub: &formattest.Publisher{}}
	f.w = NewTrashPurgeWorker(f.db, f.blobs, log, interval)
	f.w.SetPublisher(f.pub)

	t.Cleanup(f.w.Stop)

	return f
}

func (f *purgeFixture) putBlob(t *testing.T, sha string) {
	t.Helper()

	if _, err := f.blobs.Put(context.Background(), bytes.NewReader([]byte(sha)), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
}

func (f *purgeFixture) hasBlob(t *testing.T, sha string) bool {
	t.Helper()

	ok, err := f.blobs.Exists(context.Background(), sha)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}

	return ok
}

func TestTrashPurgeRunOncePurgesExpiredItems(t *testing.T) {
	f := newPurgeFixture(t, 0)

	reg := &metrics.Registry{TrashPurgedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "purged"}, []string{"kind"})}
	f.w.WithMetrics(reg)

	shaOwn, shaShared, shaOrphan := trashSHA("a"), trashSHA("b"), trashSHA("c")
	for _, sha := range []string{shaOwn, shaShared, shaOrphan} {
		f.putBlob(t, sha)
	}

	raw := models.TrashRef{Kind: models.TrashKindRaw, ID: "raw-1"}
	maven := models.TrashRef{Kind: models.TrashKindMaven, ID: "mvn-1"}
	repo := models.TrashRef{Kind: models.TrashKindRepository, ID: "repo-3"}

	f.db.expired = []models.TrashRef{raw, maven, repo}
	f.db.results[raw] = &models.TrashPurgeResult{
		Kind: raw.Kind, ID: raw.ID, RepositoryID: "repo-1", RepositoryName: "files", Format: "raw",
		Name: "docs/a.txt", SizeBytes: 10, BlobSHA256: []string{shaOwn},
	}
	// The shared blob is still referenced by a live row of another repository;
	// the duplicate entry must not be freed (or counted) twice.
	f.db.results[maven] = &models.TrashPurgeResult{
		Kind: maven.Kind, ID: maven.ID, RepositoryID: "repo-2", RepositoryName: "libs", Format: "maven",
		Name: "g/a/1.0/a-1.0.jar", Version: "1.0", SizeBytes: 200, BlobSHA256: []string{shaShared, shaShared},
		QuotaArtifacts: 1, QuotaBytes: 200,
	}
	f.db.results[repo] = &models.TrashPurgeResult{
		Kind: repo.Kind, ID: repo.ID, RepositoryID: repo.ID, RepositoryName: "gone", Format: "maven", Name: "gone",
	}
	f.db.refCounts[shaShared] = 1
	// What the repository's cascade left unreferenced.
	f.db.orphans = []string{shaOrphan}

	al := audit.NewLogger(f.db, testLogger(t), nil, 16)
	audit.SetDefault(al)

	t.Cleanup(func() { audit.SetDefault(nil) })

	stats, err := f.w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if err := al.Shutdown(context.Background()); err != nil {
		t.Fatalf("audit shutdown: %v", err)
	}

	want := PurgeStats{Purged: 3, Repositories: 1, OrphansRemoved: 1}
	if stats != want {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}

	if got := f.db.purgedRefs(); len(got) != 3 {
		t.Fatalf("purged %v, want all three items", got)
	}

	assertPurgeCleanup(t, f, shaOwn, shaShared, shaOrphan)
	assertPurgeEvents(t, f.pub.Events())
	assertPurgeAudit(t, f.db.audits)

	if got := testutil.ToFloat64(reg.TrashPurgedTotal.WithLabelValues(string(models.TrashKindRaw))); got != 1 {
		t.Errorf("purged counter for raw = %v, want 1", got)
	}
}

func assertPurgeCleanup(t *testing.T, f *purgeFixture, shaOwn, shaShared, shaOrphan string) {
	t.Helper()

	if f.hasBlob(t, shaOwn) {
		t.Error("blob of the purged raw item was not freed")
	}

	if !f.hasBlob(t, shaShared) {
		t.Error("blob still referenced by another row was deleted")
	}

	if f.hasBlob(t, shaOrphan) {
		t.Error("orphan left by the repository purge was not collected")
	}

	if len(f.db.quota) != 1 || f.db.quota[0] != (purgeQuotaCall{"repo-2", 200, 1}) {
		t.Errorf("quota releases = %+v, want one of (repo-2, 200, 1)", f.db.quota)
	}
}

func assertPurgeEvents(t *testing.T, events []formattest.Event) {
	t.Helper()

	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}

	want := []string{webhook.EventArtifactPurged, webhook.EventArtifactPurged, webhook.EventRepoPurged}
	if fmt.Sprint(types) != fmt.Sprint(want) {
		t.Fatalf("webhook events = %v, want %v", types, want)
	}

	mvn := events[1]
	if mvn.RepoID != "repo-2" || mvn.Payload["repository"] != "libs" || mvn.Payload["format"] != "maven" {
		t.Errorf("artifact.purged payload = %+v", mvn)
	}

	if a := mvn.Artifact(); a["name"] != "g/a/1.0/a-1.0.jar" || a["version"] != "1.0" || a["size"] != int64(200) {
		t.Errorf("artifact.purged artifact = %+v", a)
	}

	repoEvent := events[2]
	if repoEvent.Payload["repository"] != "gone" {
		t.Errorf("repository.purged payload = %+v", repoEvent.Payload)
	}

	if _, has := repoEvent.Payload["type"]; has {
		t.Errorf("repository.purged carries a repository type of a repository that no longer exists: %+v", repoEvent.Payload)
	}
}

func assertPurgeAudit(t *testing.T, logs []*models.AuditLog) {
	t.Helper()

	if len(logs) != 3 {
		t.Fatalf("audit events = %d, want 3", len(logs))
	}

	actions := map[string]int{}

	for _, l := range logs {
		actions[l.Action]++

		if l.ActorType == nil || *l.ActorType != "system" {
			t.Errorf("%s attributed to %v, want the system", l.Action, l.ActorType)
		}
	}

	if actions["artifact.purged"] != 2 || actions["repository.purged"] != 1 {
		t.Errorf("audit actions = %v", actions)
	}
}

func TestTrashPurgeFailingItemDoesNotAbortPass(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"database error", errTestBoom},
		{"still referenced", &pgconn.PgError{Code: "23503"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPurgeFixture(t, 0)

			bad := models.TrashRef{Kind: models.TrashKindRepository, ID: "bad"}
			good := models.TrashRef{Kind: models.TrashKindRaw, ID: "good"}

			f.db.expired = []models.TrashRef{bad, good}
			f.db.failures[bad] = tt.err

			stats, err := f.w.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			if stats.Purged != 1 || stats.Failed != 1 {
				t.Errorf("stats = %+v, want 1 purged and 1 failed", stats)
			}

			if got := f.db.attemptCount(bad); got != 1 {
				t.Errorf("failing item attempted %d times in one pass, want 1", got)
			}

			if got := f.db.purgedRefs(); len(got) != 1 || got[0] != good {
				t.Errorf("purged %v, want only the good item", got)
			}
		})
	}
}

// A backlog of failing items longer than one listing batch must not hide the
// purgeable items behind it, and none of them may be retried within the pass.
func TestTrashPurgeSkipsFailingBacklogLongerThanBatch(t *testing.T) {
	f := newPurgeFixture(t, 0)

	for i := range trashPurgeBatch + 1 {
		ref := models.TrashRef{Kind: models.TrashKindRepository, ID: fmt.Sprintf("bad-%03d", i)}
		f.db.expired = append(f.db.expired, ref)
		f.db.failures[ref] = errTestBoom
	}

	good := models.TrashRef{Kind: models.TrashKindRaw, ID: "good"}
	f.db.expired = append(f.db.expired, good)

	stats, err := f.w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if stats.Purged != 1 || stats.Failed != trashPurgeBatch+1 {
		t.Fatalf("stats = %+v, want 1 purged and %d failed", stats, trashPurgeBatch+1)
	}

	for ref, n := range f.db.attempts {
		if n != 1 {
			t.Errorf("%v attempted %d times, want 1", ref, n)
		}
	}
}

func TestTrashPurgeGoneItemIsNotAFailure(t *testing.T) {
	f := newPurgeFixture(t, 0)

	gone := models.TrashRef{Kind: models.TrashKindRaw, ID: "gone"}
	f.db.expired = []models.TrashRef{gone}
	f.db.failures[gone] = fmt.Errorf("purge: %w", database.ErrTrashNotFound)

	stats, err := f.w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if stats != (PurgeStats{}) {
		t.Errorf("stats = %+v; an item restored or purged meanwhile counts for nothing", stats)
	}
}

func TestTrashPurgeStopsAtItemCap(t *testing.T) {
	f := newPurgeFixture(t, 0)

	for i := range trashPurgeMaxItems + 5 {
		f.db.expired = append(f.db.expired, models.TrashRef{Kind: models.TrashKindRaw, ID: fmt.Sprintf("raw-%04d", i)})
	}

	stats, err := f.w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if stats.Purged != trashPurgeMaxItems || !stats.Capped {
		t.Fatalf("stats = %+v, want %d purged and Capped", stats, trashPurgeMaxItems)
	}

	// The next pass continues with what is left.
	stats, err = f.w.RunOnce(context.Background())
	if err != nil || stats.Purged != 5 || stats.Capped {
		t.Fatalf("second pass = %+v, %v; want the remaining 5, not capped", stats, err)
	}
}

func TestTrashPurgeRespectsContextCancellation(t *testing.T) {
	f := newPurgeFixture(t, 0)

	for i := range 3 {
		f.db.expired = append(f.db.expired, models.TrashRef{Kind: models.TrashKindRaw, ID: fmt.Sprintf("raw-%d", i)})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The context ends while the first item is being purged.
	f.db.onPurge = func(models.TrashRef) { cancel() }

	stats, err := f.w.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce error = %v, want context.Canceled", err)
	}

	if got := len(f.db.purgedRefs()); got != 1 || stats.Purged != 1 {
		t.Errorf("purged %d (stats %+v) items after cancellation, want only the one in flight", got, stats)
	}

	if f.db.orphanQueries != 0 {
		t.Error("orphan sweep ran with a cancelled context")
	}
}

func TestTrashPurgeSkipsWhenLockIsHeldElsewhere(t *testing.T) {
	f := newPurgeFixture(t, 0)
	f.db.expired = []models.TrashRef{{Kind: models.TrashKindRaw, ID: "raw-1"}}

	released := false
	f.w.lock = func(context.Context) (func(), bool, error) { return func() { released = true }, false, nil }

	stats, err := f.w.RunOnce(context.Background())
	if err != nil || !stats.Busy || stats.Purged != 0 {
		t.Fatalf("RunOnce = %+v, %v; want Busy and nothing purged", stats, err)
	}

	if len(f.db.limits) != 0 {
		t.Error("expired items were listed without the lock")
	}

	if released {
		t.Error("release of a lock that was never acquired was called")
	}
}

func TestTrashPurgeHoldsLockForThePassAndReleasesIt(t *testing.T) {
	f := newPurgeFixture(t, 0)
	f.db.expired = []models.TrashRef{{Kind: models.TrashKindRaw, ID: "raw-1"}}

	var held, releasedAfterPurge bool

	f.w.lock = func(context.Context) (func(), bool, error) {
		held = true

		return func() { releasedAfterPurge = len(f.db.purgedRefs()) == 1 }, true, nil
	}

	if _, err := f.w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !held || !releasedAfterPurge {
		t.Errorf("lock held = %v, released after the purge = %v; want both", held, releasedAfterPurge)
	}
}

func TestTrashPurgeLockErrorIsReturned(t *testing.T) {
	f := newPurgeFixture(t, 0)
	f.w.lock = func(context.Context) (func(), bool, error) { return func() {}, false, errTestBoom }

	if _, err := f.w.RunOnce(context.Background()); !errors.Is(err, errTestBoom) {
		t.Fatalf("RunOnce error = %v, want the lock error", err)
	}
}

func TestTrashPurgeNotATrashStoreIsANoOp(t *testing.T) {
	db := &orphanListerDB{}
	w := NewTrashPurgeWorker(db, &countingStore{}, testLogger(t), time.Millisecond)

	stats, err := w.RunOnce(context.Background())
	if err != nil || stats != (PurgeStats{}) {
		t.Fatalf("RunOnce = %+v, %v; want a no-op", stats, err)
	}

	// Start must not schedule anything either; Stop returns at once.
	w.Start()
	w.Stop()

	if db.calls != 0 {
		t.Errorf("orphan listing ran %d times for a database without trash support", db.calls)
	}
}

func TestTrashPurgeDisabledIntervalOnlyRunsOnDemand(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			f := newPurgeFixture(t, interval)
			f.db.expired = []models.TrashRef{{Kind: models.TrashKindRaw, ID: "raw-1"}}

			f.w.Start()
			// Idempotent in both directions.
			f.w.Start()
			f.w.Stop()
			f.w.Stop()

			if got := len(f.db.purgedRefs()); got != 0 {
				t.Fatalf("a disabled schedule purged %d items", got)
			}

			stats, err := f.w.RunOnce(context.Background())
			if err != nil || stats.Purged != 1 {
				t.Fatalf("on-demand RunOnce = %+v, %v; want 1 purged", stats, err)
			}
		})
	}
}

func TestTrashPurgeStopWithoutStartDoesNotBlock(t *testing.T) {
	f := newPurgeFixture(t, time.Hour)

	done := make(chan struct{})

	go func() {
		f.w.Stop()
		f.w.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(queueTestDeadline):
		t.Fatal("Stop without Start blocked")
	}

	// A late Start must not resurrect the schedule.
	f.w.Start()
}

func TestTrashPurgeScheduleRunsPassesUntilStopped(t *testing.T) {
	f := newPurgeFixture(t, 5*time.Millisecond)
	f.db.expired = []models.TrashRef{{Kind: models.TrashKindRaw, ID: "raw-1"}}

	f.w.Start()

	pollUntil(t, "the scheduled pass to purge the item", func() bool { return len(f.db.purgedRefs()) == 1 })

	f.w.Stop()
	f.w.Stop()

	// Stop waits for the schedule goroutine: it has exited when Stop returns.
	select {
	case <-f.w.doneCh:
	default:
		t.Fatal("the schedule goroutine was still running after Stop returned")
	}
}
