// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var errPrimaryOutage = errors.New("primary outage")

// lostFromPrimary replicates a blob and then removes it from the primary store.
func lostFromPrimary(t *testing.T, r *rig, data string) string {
	t.Helper()

	id := r.put(data)
	r.drain()

	if err := r.primary.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	return id
}

func TestReadFallback(t *testing.T) {
	t.Parallel()

	outage := errPrimaryOutage

	tests := []struct {
		name string
		mode string
		// primaryErr, when set, makes every primary call fail with it; otherwise
		// the blob is simply missing from the primary.
		primaryErr *error
		wantServed bool
	}{
		{"notfound mode, blob missing", storage.ReadFallbackNotFound, nil, true},
		{"notfound mode, primary outage", storage.ReadFallbackNotFound, &outage, false},
		{"error mode, blob missing", storage.ReadFallbackError, nil, true},
		{"error mode, primary outage", storage.ReadFallbackError, &outage, true},
		{"off mode, blob missing", storage.ReadFallbackOff, nil, false},
		{"off mode, primary outage", storage.ReadFallbackOff, &outage, false},
	}

	calls := []struct {
		name string
		call func(s *Store, id string) (bool, error)
	}{
		{"get", func(s *Store, id string) (bool, error) {
			rc, err := s.Get(context.Background(), id)
			if err == nil {
				_ = rc.Close()
			}

			return err == nil, err
		}},
		{"size", func(s *Store, id string) (bool, error) {
			n, err := s.GetSize(context.Background(), id)
			return err == nil && n > 0, err
		}},
		{"metadata", func(s *Store, id string) (bool, error) {
			_, err := s.GetMetadata(context.Background(), id)

			return err == nil, err
		}},
		{"exists", func(s *Store, id string) (bool, error) { ok, err := s.Exists(context.Background(), id); return ok, err }},
		{"exists by sha", func(s *Store, id string) (bool, error) {
			_, ok, err := s.ExistsBySHA256(context.Background(), id)

			return ok, err
		}},
	}

	for _, tc := range tests {
		for _, c := range calls {
			t.Run(tc.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()

				r := newRig(t, func(o *Options) { o.ReadFallback = Fallback(tc.mode) })
				id := lostFromPrimary(t, r, "fallback me")

				if tc.primaryErr != nil {
					r.primary.getErr.Store(tc.primaryErr)
				}

				served, err := c.call(r.store, id)
				if served != tc.wantServed {
					t.Fatalf("served = %v (err %v), want %v", served, err, tc.wantServed)
				}

				if !tc.wantServed && tc.primaryErr == nil && c.name != "exists" && c.name != "exists by sha" && c.name != "metadata" && !errors.Is(err, storage.ErrBlobNotFound) {
					t.Fatalf("err = %v, want the primary's not-found", err)
				}

				if tc.primaryErr != nil && !tc.wantServed && !errors.Is(err, errPrimaryOutage) && c.name != "exists by sha" {
					t.Fatalf("err = %v, want the primary's outage error", err)
				}

				want := 0.0
				if tc.wantServed {
					want = 1
				}

				if got := testutil.ToFloat64(r.m.FallbackReads.WithLabelValues(callLabel(c.name))); got != want {
					t.Fatalf("fallback_reads_total = %v, want %v", got, want)
				}
			})
		}
	}
}

func callLabel(name string) string {
	if name == "exists by sha" {
		return "exists"
	}

	return name
}

func TestFallbackReturnsReplicaBytes(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := lostFromPrimary(t, r, "served from the replica")

	if got := readAll(t, r.store, id); got != "served from the replica" {
		t.Fatalf("content = %q", got)
	}
}

func TestFallbackNeverResurrectsADeletedBlob(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := r.put("deleted")
	r.drain()

	if err := r.store.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	// The deletion is queued, the replica still has the bytes.
	if !has(t, r.replica, id) {
		t.Fatal("setup: the replica copy is already gone")
	}

	if _, err := r.store.Get(context.Background(), id); !errors.Is(err, storage.ErrBlobNotFound) {
		t.Fatalf("get of a deleted blob = %v, want not found", err)
	}

	if ok, _ := r.store.Exists(context.Background(), id); ok {
		t.Fatal("exists of a deleted blob")
	}

	if got := testutil.ToFloat64(r.m.FallbackReads.WithLabelValues("get")); got != 0 {
		t.Fatalf("fallback served a deleted blob (%v)", got)
	}

	// A repair that was already queued must not bring it back either.
	if _, err := r.svc.core.restorePrimary(context.Background(), id, false); !errors.Is(err, errPendingDelete) {
		t.Fatalf("restorePrimary = %v, want errPendingDelete", err)
	}

	if has(t, r.primary, id) {
		t.Fatal("a deleted blob was written back to the primary")
	}
}

func TestFallbackReadRepairsThePrimary(t *testing.T) {
	t.Parallel()

	calls := []struct {
		name string
		call func(s *Store, id string)
	}{
		{"get", func(s *Store, id string) {
			if rc, err := s.Get(context.Background(), id); err == nil {
				_ = rc.Close()
			}
		}},
		{"exists", func(s *Store, id string) { _, _ = s.Exists(context.Background(), id) }},
		{"exists by sha", func(s *Store, id string) { _, _, _ = s.ExistsBySHA256(context.Background(), id) }},
	}

	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t, nil)
			id := lostFromPrimary(t, r, "repair "+c.name)

			c.call(r.store, id)
			c.call(r.store, id) // coalesced

			select {
			case queued := <-r.svc.repairs.ch:
				if queued != id {
					t.Fatalf("repair queued %s", queued)
				}

				r.svc.repairs.repair(context.Background(), queued)
			default:
				t.Fatal("no repair was scheduled")
			}

			select {
			case again := <-r.svc.repairs.ch:
				t.Fatalf("the repair of %s was scheduled twice", again)
			default:
			}

			if got := readAll(t, r.primary, id); got != "repair "+c.name {
				t.Fatalf("primary content = %q", got)
			}

			if got := testutil.ToFloat64(r.m.VerifyRepaired.WithLabelValues("primary")); got != 1 {
				t.Fatalf("repaired{primary} = %v", got)
			}

			if r.queue.size() != 0 {
				t.Fatal("a repair queued replication work")
			}
		})
	}
}

func TestRepairQueueIsBounded(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)

	for i := range repairQueueSize + 50 {
		r.svc.repairs.schedule(sum([]byte{byte(i), byte(i >> 8)}))
	}

	if got := len(r.svc.repairs.ch); got != repairQueueSize {
		t.Fatalf("queue length = %d, want %d", got, repairQueueSize)
	}
}

func TestRepairRetriesAfterTheCoalescingWindow(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	id := sum([]byte("x"))

	r.svc.repairs.schedule(id)
	<-r.svc.repairs.ch
	r.svc.repairs.schedule(id)

	if len(r.svc.repairs.ch) != 0 {
		t.Fatal("rescheduled inside the window")
	}

	r.clock.Advance(repairRetryAfter + time.Second)
	r.svc.repairs.schedule(id)

	if len(r.svc.repairs.ch) != 1 {
		t.Fatal("not rescheduled after the window")
	}
}

func TestPresignedDownloadsOnlyForBlobsOnThePrimary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    string
		missing bool
		wantErr bool
	}{
		{"on primary", storage.ReadFallbackNotFound, false, false},
		{"missing from primary", storage.ReadFallbackNotFound, true, true},
		{"missing, fallback off", storage.ReadFallbackOff, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t, func(o *Options) { o.ReadFallback = Fallback(tc.mode) })
			r.svc.core.inner = presignStore{r.primary}
			id := r.put("presign")
			r.drain()

			if tc.missing {
				if err := r.primary.Delete(context.Background(), id); err != nil {
					t.Fatal(err)
				}
			}

			_, urlErr := r.store.PresignURL(context.Background(), id, time.Minute)
			_, getErr := r.store.PresignGet(context.Background(), id, time.Minute, storage.PresignGetOptions{})

			for _, err := range []error{urlErr, getErr} {
				if tc.wantErr != errors.Is(err, storage.ErrPresignNotSupported) || (!tc.wantErr && err != nil) {
					t.Fatalf("presign error = %v, want not-supported=%v", err, tc.wantErr)
				}
			}
		})
	}
}

// presignStore is a primary that can sign URLs.
type presignStore struct{ *faultStore }

func (presignStore) PresignURL(_ context.Context, sha string, _ time.Duration) (string, error) {
	return "https://primary.example/" + sha, nil
}

func (presignStore) PresignGet(_ context.Context, sha string, _ time.Duration, _ storage.PresignGetOptions) (string, error) {
	return "https://primary.example/" + sha, nil
}

func TestCapabilitiesAreDynamic(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil) // the primary is a plain BlobStore: no staging, multipart or presign

	if r.store.StagingSupported() || r.store.MultipartSupported() {
		t.Fatal("capabilities reported for a store that has none")
	}

	ctx := context.Background()

	if _, err := r.store.PresignStagingPut(ctx, "id", 1, time.Minute); !errors.Is(err, storage.ErrPresignNotSupported) {
		t.Fatalf("PresignStagingPut = %v", err)
	}

	if err := r.store.PromoteStaging(ctx, "id", sum(nil), "v", nil); !errors.Is(err, storage.ErrPresignNotSupported) {
		t.Fatalf("PromoteStaging = %v", err)
	}

	if err := r.store.CompleteMultipart(ctx, "id", "h", 1, 1); !errors.Is(err, storage.ErrPresignNotSupported) {
		t.Fatalf("CompleteMultipart = %v", err)
	}

	if err := r.store.DeleteCopy(ctx, "backend", sum(nil)); !errors.Is(err, storage.ErrPresignNotSupported) {
		t.Fatalf("DeleteCopy = %v", err)
	}
}

// stagingStore adds a staging area that "promotes" by writing the blob.
type stagingStore struct {
	*faultStore

	data map[string][]byte
}

func (s *stagingStore) StagingSupported() bool { return true }

func (s *stagingStore) PresignStagingPut(context.Context, string, int64, time.Duration) (storage.PresignedPut, error) {
	return storage.PresignedPut{URL: "https://staging.example"}, nil
}

func (s *stagingStore) StatStaging(context.Context, string) (storage.StagedObject, error) {
	return storage.StagedObject{}, nil
}

func (s *stagingStore) OpenStaging(context.Context, string, string) (io.ReadCloser, error) {
	return nil, storage.ErrStagingNotFound
}

func (s *stagingStore) PromoteStaging(ctx context.Context, _, sha, _ string, md map[string]string) error {
	_, err := s.Put(ctx, bytes.NewReader(s.data[sha]), md)

	return err
}

func (s *stagingStore) DeleteStaging(context.Context, string) error { return nil }

func (s *stagingStore) WalkStaging(context.Context, time.Time, func([]string) error) error {
	return nil
}

func TestPromoteStagingQueuesTheCopy(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	data := []byte("client direct upload")
	id := sum(data)
	r.svc.core.inner = &stagingStore{faultStore: r.primary, data: map[string][]byte{id: data}}

	if !r.store.StagingSupported() {
		t.Fatal("staging not forwarded")
	}

	if err := r.store.PromoteStaging(context.Background(), "staging-id", id, "v1", nil); err != nil {
		t.Fatal(err)
	}

	if op := r.queue.op(id); op == nil || op.Op != database.BlobReplicationCopy {
		t.Fatalf("promote did not queue a copy: %+v", op)
	}

	r.drain()

	if got := readAll(t, r.replica, id); got != string(data) {
		t.Fatalf("replica content = %q", got)
	}
}
