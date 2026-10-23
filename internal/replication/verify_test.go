// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// directPut writes a blob to a store behind the replication layer's back.
func directPut(t *testing.T, s *faultStore, data string) string {
	t.Helper()

	id, err := s.BlobStore.Put(context.Background(), bytes.NewReader([]byte(data)), nil)
	if err != nil {
		t.Fatal(err)
	}

	return id
}

// verifyAll requests a pass and ticks until it finishes.
func (r *rig) verifyAll() VerifyResult {
	r.t.Helper()

	if ok, err := r.svc.RequestVerify(context.Background()); err != nil || !ok {
		r.t.Fatalf("request verify: %v %v", ok, err)
	}

	for range 1000 {
		if err := r.svc.verifier.Tick(context.Background()); err != nil {
			r.t.Fatalf("tick: %v", err)
		}

		if res, ok := decodeResult(r.queue.state.LastVerify); ok && res.FinishedAt != nil && r.queue.state.VerifyRun == nil {
			return res
		}
	}

	r.t.Fatal("verify did not finish")

	return VerifyResult{}
}

//nolint:gochecknoglobals // test table, kept out of the test function to keep it simple
var verifyCases = []struct {
	name  string
	deep  bool
	setup func(t *testing.T, r *rig) (id string)
	// check asserts the repaired state.
	check func(t *testing.T, r *rig, id string, res VerifyResult)
}{
	{
		name: "missing on replica is queued and copied",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			return directPut(t, r.primary, "never replicated")
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.MissingReplica != 1 || res.Repaired != 1 {
				t.Fatalf("result = %+v", res)
			}

			r.drain()

			if got := readAll(t, r.replica, id); got != "never replicated" {
				t.Fatalf("replica content = %q", got)
			}
		},
	},
	{
		name: "missing on primary is restored when referenced",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			id := directPut(t, r.replica, "lost primary")
			r.queue.referenced[id] = true

			return id
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.MissingPrimary != 1 || res.Repaired != 1 {
				t.Fatalf("result = %+v", res)
			}

			if got := readAll(t, r.primary, id); got != "lost primary" {
				t.Fatalf("primary content = %q", got)
			}

			if got := testutil.ToFloat64(r.m.VerifyRepaired.WithLabelValues("primary")); got != 1 {
				t.Fatalf("repaired{primary} = %v", got)
			}
		},
	},
	{
		name: "replica-only blob nothing needs is reported, not restored",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			return directPut(t, r.replica, "orphan")
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.Unreferenced != 1 || res.MissingPrimary != 0 || has(t, r.primary, id) {
				t.Fatalf("result = %+v", res)
			}
		},
	},
	{
		name: "replica-only blob with a queued delete stays deleted",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			id := directPut(t, r.replica, "being deleted")
			r.queue.referenced[id] = true

			if err := r.queue.EnqueueBlobReplication(context.Background(), id, database.BlobReplicationDelete); err != nil {
				t.Fatal(err)
			}

			return id
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.MissingPrimary != 0 || has(t, r.primary, id) {
				t.Fatalf("a queued delete was undone: %+v", res)
			}
		},
	},
	{
		name: "missing on replica with a queued copy is lag, not damage",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			return r.put("still queued")
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.MissingReplica != 0 || r.queue.op(id).Version != 1 {
				t.Fatalf("result = %+v", res)
			}
		},
	},
	{
		name: "damaged replica copy is rewritten from the primary",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			id := r.put("good on primary")
			r.drain()
			damage(t, r.replica, id, "short")

			return id
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.Mismatched != 1 || res.Repaired != 1 || res.Errors != 0 {
				t.Fatalf("result = %+v", res)
			}

			if got := readAll(t, r.replica, id); got != "good on primary" {
				t.Fatalf("replica content = %q", got)
			}
		},
	},
	{
		name: "damaged primary copy is rewritten from the replica",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			id := r.put("good on replica")
			r.drain()
			damage(t, r.primary, id, "short")

			return id
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.Mismatched != 1 || res.Errors != 0 {
				t.Fatalf("result = %+v", res)
			}

			if got := readAll(t, r.primary, id); got != "good on replica" {
				t.Fatalf("primary content = %q", got)
			}
		},
	},
	{
		name: "both copies damaged is an error and nothing is overwritten",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			id := r.put("doomed")
			r.drain()
			damage(t, r.primary, id, "p")
			damage(t, r.replica, id, "rr")

			return id
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.Mismatched != 1 || res.Errors != 1 {
				t.Fatalf("result = %+v", res)
			}

			if readAll(t, r.primary, id) != "p" || readAll(t, r.replica, id) != "rr" {
				t.Fatal("a copy was overwritten although neither is right")
			}
		},
	},
	{
		name: "same size, different bytes: only a deep pass sees it",
		deep: true,
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			id := r.put("0123456789")
			r.drain()
			damage(t, r.replica, id, "012345678X")

			return id
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if !res.Deep || res.Mismatched != 1 {
				t.Fatalf("result = %+v", res)
			}

			if got := readAll(t, r.replica, id); got != "0123456789" {
				t.Fatalf("replica content = %q", got)
			}
		},
	},
	{
		name: "same size, different bytes: a shallow pass does not look",
		setup: func(t *testing.T, r *rig) string {
			t.Helper()

			id := r.put("0123456789")
			r.drain()
			damage(t, r.replica, id, "012345678X")

			return id
		},
		check: func(t *testing.T, r *rig, id string, res VerifyResult) {
			t.Helper()

			if res.Mismatched != 0 || res.Deep {
				t.Fatalf("result = %+v", res)
			}
		},
	},
}

func TestVerifyFindsAndRepairsDifferences(t *testing.T) {
	t.Parallel()

	for _, tc := range verifyCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t, func(o *Options) { o.VerifyDeep = tc.deep })
			id := tc.setup(t, r)
			res := r.verifyAll()

			if res.Checked < 1 || res.Listing != listingStore || !res.ReplicaListed {
				t.Fatalf("result = %+v", res)
			}

			tc.check(t, r, id, res)
		})
	}
}

func TestVerifyDatabaseListingWhenThePrimaryCannotList(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	r.primary.noList.Store(true)

	// A blob only the replica has and the database needs.
	onReplica := directPut(t, r.replica, "from the replica")
	r.queue.dbBlobs[onReplica], r.queue.referenced[onReplica] = true, true

	// A blob the database lists that exists nowhere.
	lost := sum([]byte("gone everywhere"))
	r.queue.dbBlobs[lost], r.queue.referenced[lost] = true, true

	// A blob both sides have.
	both := r.put("both")
	r.drain()

	r.queue.dbBlobs[both] = true

	res := r.verifyAll()

	if res.Listing != listingDatabase || res.Lost != 1 || res.MissingPrimary != 1 || res.Errors != 0 {
		t.Fatalf("result = %+v", res)
	}

	if got := readAll(t, r.primary, onReplica); got != "from the replica" {
		t.Fatalf("primary content = %q", got)
	}
}

func TestVerifyWhenTheReplicaCannotList(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	r.replica.noList.Store(true)

	id := directPut(t, r.primary, "unlisted replica")

	res := r.verifyAll()

	if res.ReplicaListed || res.MissingReplica != 1 {
		t.Fatalf("result = %+v", res)
	}

	if op := r.queue.op(id); op == nil || op.Op != database.BlobReplicationCopy {
		t.Fatalf("copy not queued: %+v", op)
	}
}

func TestVerifyResumesFromTheStoredCursor(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	r.svc.verifier.window = 2
	r.svc.verifier.budget = 0 // one window per tick

	const blobs = 7

	for i := range blobs {
		directPut(t, r.primary, fmt.Sprintf("blob %d", i))
	}

	if ok, err := r.svc.RequestVerify(context.Background()); err != nil || !ok {
		t.Fatal(ok, err)
	}

	// A second request while one is requested or running is refused.
	if ok, _ := r.svc.RequestVerify(context.Background()); ok {
		t.Fatal("a second request was accepted")
	}

	var cursors []string

	for tick := range 20 {
		if err := r.svc.verifier.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}

		if r.queue.state.VerifyRun == nil {
			break
		}

		cursors = append(cursors, r.queue.state.VerifyCursor)

		if tick%2 == 1 {
			r.svc.verifier.walk = nil // a restart loses the in-memory walk
		}

		if ok, _ := r.svc.RequestVerify(context.Background()); ok {
			t.Fatal("a request was accepted while a pass runs")
		}
	}

	res, ok := decodeResult(r.queue.state.LastVerify)
	if !ok || res.Checked != blobs || res.MissingReplica != blobs {
		t.Fatalf("result = %+v (cursors %v)", res, cursors)
	}

	for i := 1; i < len(cursors); i++ {
		if cursors[i] <= cursors[i-1] {
			t.Fatalf("cursor did not advance: %v", cursors)
		}
	}

	if len(cursors) < 3 {
		t.Fatalf("the pass was not split into windows: %v", cursors)
	}
}

func TestVerifySchedule(t *testing.T) {
	t.Parallel()

	r := newRig(t, func(o *Options) { o.VerifyInterval = time.Hour })
	ctx := context.Background()

	// Never verified: the first tick starts (and finishes) a pass, which also
	// backfills blobs that predate replication.
	id := directPut(t, r.primary, "predates replication")

	if err := r.svc.verifier.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	first, ok := decodeResult(r.queue.state.LastVerify)
	if !ok || first.MissingReplica != 1 || r.queue.op(id) == nil {
		t.Fatalf("first pass = %+v", first)
	}

	// Not due again until the interval passed.
	r.clock.Advance(59 * time.Minute)

	if err := r.svc.verifier.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if again, _ := decodeResult(r.queue.state.LastVerify); !again.StartedAt.Equal(first.StartedAt) {
		t.Fatal("a pass started before the interval elapsed")
	}

	r.clock.Advance(2 * time.Minute)

	if err := r.svc.verifier.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if again, _ := decodeResult(r.queue.state.LastVerify); !again.StartedAt.After(first.StartedAt) {
		t.Fatal("no pass started after the interval elapsed")
	}

	if got := testutil.ToFloat64(r.m.LastVerify); got != float64(r.clock.Now().Unix()) {
		t.Fatalf("last_verify_timestamp_seconds = %v", got)
	}
}

func TestVerifyOnDemandOnly(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil) // VerifyInterval 0

	if err := r.svc.verifier.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	if r.queue.state.LastVerify != nil || r.queue.state.VerifyRun != nil {
		t.Fatal("a pass ran without a request")
	}
}

func TestVerifyMetricsAndStatus(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	directPut(t, r.primary, "one")
	directPut(t, r.primary, "two")

	res := r.verifyAll()

	if got := testutil.ToFloat64(r.m.VerifyChecked); got != 2 || res.Checked != 2 {
		t.Fatalf("verify_checked_total = %v, result %+v", got, res)
	}

	if got := testutil.ToFloat64(r.m.VerifyMissing.WithLabelValues("replica")); got != 2 {
		t.Fatalf("verify_missing_total{replica} = %v", got)
	}

	if got := testutil.ToFloat64(r.m.VerifyRepaired.WithLabelValues("replica")); got != 2 {
		t.Fatalf("verify_repaired_total{replica} = %v", got)
	}

	st, err := r.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !st.Enabled || st.BacklogCopy != 2 || st.LastVerify == nil || st.LastVerify.Checked != 2 || st.VerifyRunning != nil {
		t.Fatalf("status = %+v", st)
	}

	if st.Replica.Type != "filesystem" || st.ReadFallback != "notfound" {
		t.Fatalf("status = %+v", st)
	}
}

func TestVerifyMergesTieredListings(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	cold := newFS(t)
	r.svc.core.inner = &twoTier{BlobStore: r.primary, tiers: []*faultStore{r.primary, cold}}

	// Interleaved ids across the tiers, plus one blob on both while it moves.
	var want []string

	for i := range 12 {
		data := fmt.Sprintf("tiered %d", i)
		tier := []*faultStore{r.primary, cold}[i%2]
		want = append(want, directPut(t, tier, data))
	}

	dup := directPut(t, cold, "tiered 0")
	_ = dup

	// A twoTier store reads through both tiers, so replicate everything.
	res := r.verifyAll()

	if res.Checked != 12 {
		t.Fatalf("checked %d blobs, want 12 (duplicates must be merged): %+v", res.Checked, res)
	}

	r.drain()

	for _, id := range want {
		if !has(t, r.replica, id) {
			t.Fatalf("blob %s not replicated", id)
		}
	}
}
