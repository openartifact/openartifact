// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPutReplicatesToReplica(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := r.put("hello replica")

	if op := r.queue.op(id); op == nil || op.Op != database.BlobReplicationCopy {
		t.Fatalf("queued op = %+v, want a copy", op)
	}

	if has(t, r.replica, id) {
		t.Fatal("replica has the blob before the worker ran")
	}

	r.drain()

	if got := readAll(t, r.replica, id); got != "hello replica" {
		t.Fatalf("replica content = %q", got)
	}

	if r.queue.size() != 0 {
		t.Fatalf("queue has %d rows after the copy", r.queue.size())
	}

	if st := r.queue.state; st.ReplicatedTotal != 1 || st.ReplicatedBytes != int64(len("hello replica")) {
		t.Fatalf("state = %+v", st)
	}

	if got := testutil.ToFloat64(r.m.Replicated); got != 1 {
		t.Fatalf("replicated_total = %v", got)
	}

	if got := testutil.ToFloat64(r.m.ReplicatedBytes); got != float64(len("hello replica")) {
		t.Fatalf("replicated_bytes_total = %v", got)
	}
}

func TestInvalidIDIsNotQueued(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)

	if err := r.store.Delete(context.Background(), "not-a-sha"); err == nil {
		t.Fatal("delete of an invalid id must fail")
	}

	if r.queue.size() != 0 {
		t.Fatal("an invalid id was queued")
	}
}

func TestOperationOrdering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// steps run against a blob that is on both sides already.
		steps       func(r *rig, id string, data string)
		wantReplica bool
	}{
		{"delete applied", func(r *rig, id, _ string) {
			_ = r.store.Delete(context.Background(), id)
		}, false},
		{"delete then upload again keeps the copy", func(r *rig, id, data string) {
			_ = r.store.Delete(context.Background(), id)
			r.put(data)
		}, true},
		{"upload then delete removes the copy", func(r *rig, id, data string) {
			r.put(data)
			_ = r.store.Delete(context.Background(), id)
		}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t, nil)
			id := r.put("ordering")
			r.drain()

			tc.steps(r, id, "ordering")
			r.drain()

			if got := has(t, r.replica, id); got != tc.wantReplica {
				t.Fatalf("replica has blob = %v, want %v", got, tc.wantReplica)
			}

			if got := has(t, r.primary, id); got != tc.wantReplica {
				t.Fatalf("primary has blob = %v, want %v", got, tc.wantReplica)
			}
		})
	}
}

func TestInFlightOperationSupersededIsNotLost(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := r.put("supersede")

	ops, err := r.queue.ClaimBlobReplications(context.Background(), r.clock.Now(), 10, time.Minute)
	if err != nil || len(ops) != 1 {
		t.Fatalf("claim: %v %v", ops, err)
	}

	// While the copy is in flight the blob is deleted: the delete replaces the copy.
	if err := r.store.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	if ok, _ := r.queue.CompleteBlobReplication(context.Background(), id, ops[0].Version); ok {
		t.Fatal("the superseded copy completed")
	}

	if op := r.queue.op(id); op == nil || op.Op != database.BlobReplicationDelete {
		t.Fatalf("the delete must stay queued, got %+v", op)
	}
}

func TestDeleteKeepsCopyOfBlobStillInUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(r *rig, id string)
	}{
		{"primary has the blob again", func(*rig, string) {}},
		{"blob is referenced", func(r *rig, id string) {
			if err := r.primary.Delete(context.Background(), id); err != nil {
				t.Fatal(err)
			}

			r.queue.referenced[id] = true
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t, nil)
			id := r.put("in use")
			r.drain()
			tc.setup(r, id)

			if err := r.queue.EnqueueBlobReplication(context.Background(), id, database.BlobReplicationDelete); err != nil {
				t.Fatal(err)
			}

			if _, err := r.svc.replicator.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}

			if !has(t, r.replica, id) {
				t.Fatal("the replica copy of a blob in use was deleted")
			}

			if op := r.queue.op(id); op == nil || op.Op != database.BlobReplicationCopy {
				t.Fatalf("the delete must turn into a copy, got %+v", op)
			}
		})
	}
}

func TestCopyOfMissingPrimaryBlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		referenced bool
		wantQueued bool
	}{
		{"unreferenced is dropped", false, false},
		{"referenced is retried", true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t, nil)
			id := sum([]byte("never written"))
			r.queue.referenced[id] = tc.referenced

			if err := r.queue.EnqueueBlobReplication(context.Background(), id, database.BlobReplicationCopy); err != nil {
				t.Fatal(err)
			}

			if _, err := r.svc.replicator.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}

			op := r.queue.op(id)
			if (op != nil) != tc.wantQueued {
				t.Fatalf("queued = %v, want %v", op != nil, tc.wantQueued)
			}

			if op != nil && (op.Attempts != 1 || !strings.Contains(op.LastError, "missing from the primary")) {
				t.Fatalf("op = %+v", op)
			}

			if got := testutil.ToFloat64(r.m.MissingPrimary); got != 1 {
				t.Fatalf("missing_primary_total = %v", got)
			}
		})
	}
}

func TestFailureBackoffAndRecovery(t *testing.T) {
	t.Parallel()

	r := newRig(t, func(o *Options) { o.MaxBackoff = 30 * time.Second })
	id := r.put("flaky")
	r.replica.down.Store(true)

	// Delay after n failed attempts: 5s, 10s, 20s, then capped at 30s.
	for i, wantDelay := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second} {
		if n, err := r.svc.replicator.RunOnce(context.Background()); err != nil || n != 1 {
			t.Fatalf("attempt %d: n=%d err=%v", i, n, err)
		}

		op := r.queue.op(id)
		if op == nil || op.Attempts != i+1 {
			t.Fatalf("attempt %d: op = %+v", i, op)
		}

		if got := op.NextAttemptAt.Sub(r.clock.Now()); got != wantDelay {
			t.Fatalf("attempt %d: next attempt in %v, want %v", i, got, wantDelay)
		}

		if !strings.Contains(op.LastError, "replica down") {
			t.Fatalf("attempt %d: last error %q", i, op.LastError)
		}

		// Not due before its time.
		if n, _ := r.svc.replicator.RunOnce(context.Background()); n != 0 {
			t.Fatalf("attempt %d: a backed-off operation was claimed", i)
		}

		r.clock.Advance(wantDelay)
	}

	if got := testutil.ToFloat64(r.m.Failures.WithLabelValues("copy")); got != 5 {
		t.Fatalf("failures_total = %v", got)
	}

	if r.queue.state.LastError == "" {
		t.Fatal("the shared last error was not recorded")
	}

	if b, _ := r.queue.BlobReplicationBacklog(context.Background(), failingAttempts); b.Failing != 1 {
		t.Fatalf("failing = %d, want 1", b.Failing)
	}

	r.replica.down.Store(false)
	r.drain()

	if !has(t, r.replica, id) || r.queue.size() != 0 {
		t.Fatal("the copy did not complete after the replica recovered")
	}

	if r.queue.state.LastError != "" {
		t.Fatalf("last error not cleared: %q", r.queue.state.LastError)
	}
}

func TestBackoffTable(t *testing.T) {
	t.Parallel()

	r := newRig(t, func(o *Options) { o.MaxBackoff = time.Hour })

	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 5 * time.Second}, {1, 10 * time.Second}, {2, 20 * time.Second}, {5, 160 * time.Second},
		{9, 2560 * time.Second}, {10, time.Hour}, {1000, time.Hour}, {-3, 5 * time.Second},
	}

	for _, tc := range tests {
		if got := r.svc.core.backoff(tc.attempts); got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.attempts, got, tc.want)
		}
	}

	// With the real jitter the delay stays in [d/2, d].
	for range 200 {
		if d := defaultJitter(time.Minute); d < 30*time.Second || d > time.Minute {
			t.Fatalf("jitter(1m) = %v", d)
		}
	}
}

func TestTruncatedErrorsAreStored(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := r.put("long error")

	long := errors.New(strings.Repeat("x", 5000))
	r.replica.getErr.Store(&long)
	r.replica.failPut.Store(true)

	if _, err := r.svc.replicator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if op := r.queue.op(id); op == nil || len(op.LastError) > maxErrorLen+3 {
		t.Fatalf("stored error length: %+v", op)
	}
}

func TestCopyReplacesDamagedReplicaCopy(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := r.put("good bytes")
	r.drain()

	damage(t, r.replica, id, "bad")
	// A second copy is queued (a re-upload); the worker notices the size difference.
	if err := r.queue.EnqueueBlobReplication(context.Background(), id, database.BlobReplicationCopy); err != nil {
		t.Fatal(err)
	}

	r.drain()

	if got := readAll(t, r.replica, id); got != "good bytes" {
		t.Fatalf("replica copy = %q", got)
	}
}

func TestConcurrentUploadsStartNoGoroutines(t *testing.T) {
	// Not parallel: it counts goroutines.
	r := newRig(t, nil)

	before := runtime.NumGoroutine()

	const uploads = 1000

	var wg sync.WaitGroup

	for i := range uploads {
		wg.Go(func() {
			if _, err := r.store.Put(context.Background(), bytes.NewReader([]byte(fmt.Sprintf("blob-%d", i))), nil); err != nil {
				t.Errorf("put %d: %v", i, err)
			}
		})
	}

	wg.Wait()

	if r.queue.size() != uploads {
		t.Fatalf("queued %d, want %d", r.queue.size(), uploads)
	}

	// The upload goroutines above are gone; the store itself keeps none.
	eventually(t, "upload goroutines to exit", func() bool { return runtime.NumGoroutine() <= before+2 })
}

func TestEnqueueFailure(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	r.queue.failEnq.Store(true)

	id, err := r.store.Put(context.Background(), strings.NewReader("queue down"), nil)
	if err != nil {
		t.Fatalf("a queue failure must not fail the upload: %v", err)
	}

	if !has(t, r.primary, id) {
		t.Fatal("blob not stored")
	}

	if got := testutil.ToFloat64(r.m.EnqueueFailures); got != 1 {
		t.Fatalf("enqueue_failures_total = %v", got)
	}

	// A delete that cannot be queued is reported, so the deletion is retried.
	if err := r.store.Delete(context.Background(), id); err == nil {
		t.Fatal("delete must fail when the replica deletion cannot be queued")
	}
}

func TestBackgroundWorkerReplicates(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	r.svc.Start()

	ids := make([]string, 0, 10)
	for i := range 10 {
		ids = append(ids, r.put(fmt.Sprintf("background %d", i)))
	}

	eventually(t, "the blobs to be replicated", func() bool {
		for _, id := range ids {
			if !has(t, r.replica, id) {
				return false
			}
		}

		return r.queue.size() == 0
	})

	r.svc.Stop()
}

func TestStopReleasesUnstartedClaims(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := r.put("stopping")

	r.svc.replicator.Stop()

	if n, err := r.svc.replicator.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	// The claim was handed back: the operation is due again at once.
	if ops, _ := r.queue.ClaimBlobReplications(context.Background(), r.clock.Now(), 10, time.Minute); len(ops) != 1 || ops[0].SHA256 != id {
		t.Fatalf("claims after stop = %v", ops)
	}
}
