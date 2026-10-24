// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
)

const auditRetentionDays = 30

// recordingAuditStore wraps the real store: it records the size of every
// batch and calls hook after each one (with the 1-based call number), which
// lets a test observe or interfere with a pass in flight.
type recordingAuditStore struct {
	database.AuditRetentionStore

	batches []int64
	hook    func(call int)
}

func (r *recordingAuditStore) PurgeAuditLogsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	n, err := r.AuditRetentionStore.PurgeAuditLogsBefore(ctx, cutoff, limit)
	r.batches = append(r.batches, n)

	if r.hook != nil {
		r.hook(len(r.batches))
	}

	return n, err
}

// newAuditRetentionTestWorker builds a worker over pg with the retention of
// the test, a tiny pacing pause and a recording store.
func newAuditRetentionTestWorker(t *testing.T, pg *database.PostgresDB, batch int) (*AuditRetentionWorker, *recordingAuditStore) {
	t.Helper()

	w := NewAuditRetentionWorker(pg, testLogger(t), auditRetentionDays, time.Hour, batch)
	w.pause = time.Millisecond

	rec := &recordingAuditStore{AuditRetentionStore: pg}
	w.store = rec

	t.Cleanup(w.Stop)

	return w, rec
}

// seedAudit inserts n audit rows aged from first, first+step, first+2*step, ...
// (older and older) and returns nothing; index 0 is the youngest.
func seedAudit(t *testing.T, pg *database.PostgresDB, n int, first, step time.Duration) {
	t.Helper()

	_, err := pg.GetDB().ExecContext(context.Background(),
		`INSERT INTO audit_logs (action, actor_type, created_at)
		 SELECT 'http.request', 'system', now() - make_interval(secs => $1::float8 + i * $2::float8)
		 FROM generate_series(0, $3::int - 1) AS i`,
		first.Seconds(), step.Seconds(), n)
	if err != nil {
		t.Fatalf("seed audit rows: %v", err)
	}
}

func auditRows(t *testing.T, pg *database.PostgresDB, where string) int {
	t.Helper()

	var n int
	if err := pg.GetDB().QueryRowContext(context.Background(), `SELECT count(*) FROM audit_logs WHERE `+where).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}

	return n
}

func TestAuditRetention_DeletesOnlyOlderRowsInBoundedBatches(t *testing.T) {
	const batch = 100

	for _, tc := range []struct {
		name        string
		old         int
		wantBatches []int64
	}{
		{"partial last batch", 250, []int64{100, 100, 50}},
		{"exact multiple ends on an empty batch", 200, []int64{100, 100, 0}},
		{"less than one batch", 7, []int64{7}},
		{"nothing old", 0, []int64{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pg := newTrashPurgeTestDB(t)
			w, rec := newAuditRetentionTestWorker(t, pg, batch)

			// Expired: 31 days and older. Kept: just inside the window, a day
			// old and brand new.
			seedAudit(t, pg, tc.old, 31*24*time.Hour, time.Minute)
			seedAudit(t, pg, 5, 29*24*time.Hour, time.Hour)
			seedAudit(t, pg, 1, 0, 0)

			const kept = 6

			stats, err := w.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			if stats.Deleted != int64(tc.old) || stats.Busy {
				t.Errorf("stats = %+v, want %d deleted and not busy", stats, tc.old)
			}

			if !equalInt64(rec.batches, tc.wantBatches) || stats.Batches != len(tc.wantBatches) {
				t.Errorf("batches = %v (stats %d), want %v", rec.batches, stats.Batches, tc.wantBatches)
			}

			for _, n := range rec.batches {
				if n > batch {
					t.Errorf("a batch deleted %d rows, more than the bound %d", n, batch)
				}
			}

			if got := auditRows(t, pg, "true"); got != kept {
				t.Errorf("%d rows left, want the %d inside the retention", got, kept)
			}

			if got := auditRows(t, pg, "created_at < now() - interval '30 days'"); got != 0 {
				t.Errorf("%d expired rows survived", got)
			}
		})
	}
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestAuditRetention_DeletesOldestFirstAndKeepsProgressWhenInterrupted(t *testing.T) {
	pg := newTrashPurgeTestDB(t)
	w, rec := newAuditRetentionTestWorker(t, pg, 100)

	// 250 expired rows, one minute apart: row i is 40 days and i minutes old.
	seedAudit(t, pg, 250, 40*24*time.Hour, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec.hook = func(call int) {
		if call == 1 {
			// The 100 oldest are gone, the 150 youngest are all still there.
			if got := auditRows(t, pg, "created_at < now() - interval '40 days' - interval '149 minutes' - interval '1 second'"); got != 0 {
				t.Errorf("%d rows older than the 150 youngest survived the first batch", got)
			}

			if got := auditRows(t, pg, "true"); got != 150 {
				t.Errorf("%d rows after the first batch, want 150", got)
			}

			cancel()
		}
	}

	stats, err := w.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce error = %v, want context.Canceled", err)
	}

	if stats.Deleted != 100 || stats.Batches != 1 {
		t.Errorf("stats = %+v, want 100 deleted in 1 batch", stats)
	}

	// The next pass continues with what is left.
	rec.hook = nil

	stats, err = w.RunOnce(context.Background())
	if err != nil || stats.Deleted != 150 {
		t.Fatalf("second pass = %+v, %v; want the remaining 150 deleted", stats, err)
	}

	if got := auditRows(t, pg, "true"); got != 0 {
		t.Errorf("%d rows left", got)
	}
}

func TestAuditRetention_ZeroRetentionKeepsEverything(t *testing.T) {
	pg := newTrashPurgeTestDB(t)

	seedAudit(t, pg, 20, 1000*24*time.Hour, time.Hour)

	w := NewAuditRetentionWorker(pg, testLogger(t), 0, time.Hour, 10)
	t.Cleanup(w.Stop)

	w.Start() // must be a no-op, not a goroutine deleting everything

	stats, err := w.RunOnce(context.Background())
	if err != nil || stats != (AuditRetentionStats{}) {
		t.Fatalf("RunOnce = %+v, %v; want a no-op", stats, err)
	}

	if got := auditRows(t, pg, "true"); got != 20 {
		t.Errorf("%d rows left, want all 20 kept", got)
	}
}

func TestAuditRetention_TwoReplicasDoNotRunTogether(t *testing.T) {
	pg := newTrashPurgeTestDB(t)

	seedAudit(t, pg, 300, 60*24*time.Hour, time.Minute)
	seedAudit(t, pg, 3, 0, 0)

	a, aRec := newAuditRetentionTestWorker(t, pg, 100)
	b, bRec := newAuditRetentionTestWorker(t, pg, 100)

	inBatch := make(chan struct{})
	release := make(chan struct{})

	// Replica A stops inside its first batch, holding the lock.
	aRec.hook = func(call int) {
		if call == 1 {
			close(inBatch)
			<-release
		}
	}

	type result struct {
		stats AuditRetentionStats
		err   error
	}

	aDone := make(chan result, 1)

	go func() {
		stats, err := a.RunOnce(context.Background())
		aDone <- result{stats, err}
	}()

	<-inBatch

	// While A is mid-pass, replica B must find the lock taken and do nothing.
	bStats, err := b.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("replica B: %v", err)
	}

	if !bStats.Busy || bStats.Deleted != 0 || len(bRec.batches) != 0 {
		t.Errorf("replica B ran next to A: stats %+v, batches %v", bStats, bRec.batches)
	}

	close(release)

	aRes := <-aDone
	if aRes.err != nil || aRes.stats.Deleted != 300 || aRes.stats.Busy {
		t.Fatalf("replica A = %+v, %v; want all 300 deleted", aRes.stats, aRes.err)
	}

	// The lock is free again: B may run (and finds nothing left).
	bStats, err = b.RunOnce(context.Background())
	if err != nil || bStats.Busy || bStats.Deleted != 0 {
		t.Errorf("replica B after A = %+v, %v; want a clean empty pass", bStats, err)
	}

	if got := auditRows(t, pg, "true"); got != 3 {
		t.Errorf("%d rows left, want the 3 recent ones", got)
	}
}

func TestAuditRetention_StartPurgesAndStopIsClean(t *testing.T) {
	pg := newTrashPurgeTestDB(t)

	seedAudit(t, pg, 40, 45*24*time.Hour, time.Minute)
	seedAudit(t, pg, 2, 0, 0)

	w := NewAuditRetentionWorker(pg, testLogger(t), auditRetentionDays, time.Hour, 15)
	w.pause = time.Millisecond

	w.Start()

	deadline := time.Now().Add(30 * time.Second)

	for auditRows(t, pg, "true") != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("scheduled pass left %d rows, want 2", auditRows(t, pg, "true"))
		}

		time.Sleep(20 * time.Millisecond)
	}

	w.Stop()
	w.Stop() // idempotent
}

func TestAuditRetention_Metrics(t *testing.T) {
	pg := newTrashPurgeTestDB(t)
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	w, _ := newAuditRetentionTestWorker(t, pg, 50)
	w.WithMetrics(reg)

	seedAudit(t, pg, 120, 31*24*time.Hour, time.Minute)

	if got := testutil.ToFloat64(reg.AuditRetentionLastRunSeconds); got != 0 {
		t.Fatalf("last run gauge = %v before any pass, want 0", got)
	}

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := testutil.ToFloat64(reg.AuditRetentionDeletedTotal); got != 120 {
		t.Errorf("deleted counter = %v, want 120", got)
	}

	if got := testutil.ToFloat64(reg.AuditRetentionLastRunSeconds); time.Since(time.Unix(int64(got), 0)) > time.Minute {
		t.Errorf("last run gauge = %v, want about now", got)
	}
}
