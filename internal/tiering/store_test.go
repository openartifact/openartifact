// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

// moveTo puts the blob on the cold tier through the real mover.
func (r *rig) moveCold(sha string) {
	r.t.Helper()

	if out, err := r.mover.Move(context.Background(), sha, testColdBackend, "test"); err != nil || out != OutcomeMoved {
		r.t.Fatalf("move to cold = %v, %v", out, err)
	}

	r.sweep()
}

func TestStoreReadsAreTransparentAcrossTiers(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()
	data := []byte("served from wherever it lives")
	sha := r.addBlob(data, 0)

	r.moveCold(sha)
	r.restart() // cold caches, nothing remembers where the blob is

	r.mustReadable(sha, data)

	if n, err := r.store.GetSize(ctx, sha); err != nil || n != int64(len(data)) {
		t.Errorf("GetSize = %d, %v", n, err)
	}

	if ok, err := r.store.Exists(ctx, sha); err != nil || !ok {
		t.Errorf("Exists = %v, %v", ok, err)
	}

	if id, ok, err := r.store.ExistsBySHA256(ctx, sha); err != nil || !ok || id != sha {
		t.Errorf("ExistsBySHA256 = %q, %v, %v", id, ok, err)
	}

	if _, err := r.store.GetMetadata(ctx, sha); err != nil {
		t.Errorf("GetMetadata = %v", err)
	}

	if ok, err := r.store.Exists(ctx, "ab"+sha[2:]); err != nil || ok {
		t.Errorf("Exists(unknown) = %v, %v; want false, nil", ok, err)
	}

	if _, err := r.store.Get(ctx, "ab"+sha[2:]); !errors.Is(err, storage.ErrBlobNotFound) {
		t.Errorf("Get(unknown) = %v, want ErrBlobNotFound", err)
	}
}

// A blob whose bytes sit on a tier the database does not name (a placement flip
// the store has not seen, or an unreachable database) is still found.
func TestStoreFindsBytesWhenPlacementIsStale(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	data := []byte("placement says hot, bytes say cold")
	sha := r.addBlob(data, 0)

	r.moveCold(sha)

	r.db.mu.Lock()
	r.db.blobs[sha].BackendID = "" // stale: bytes are only on cold
	r.db.mu.Unlock()
	r.restart()

	r.mustReadable(sha, data)
}

func TestStorePutLandsOnPlacementTier(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()
	data := []byte("uploaded twice")
	sha := r.addBlob(data, 0)

	r.moveCold(sha)

	// Re-upload of a cold-placed blob must not create a second (hot) copy.
	if _, err := r.store.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if hot, cold, placement := r.where(sha); hot || !cold || placement != testColdBackend {
		t.Errorf("after re-upload: hot=%v cold=%v placement=%q", hot, cold, placement)
	}

	// A new blob lands on the primary store.
	fresh := []byte("brand new")

	newSha, err := r.store.Put(ctx, bytes.NewReader(fresh), nil)
	if err != nil {
		t.Fatal(err)
	}

	if hot, cold, _ := r.where(newSha); !hot || cold {
		t.Errorf("new blob: hot=%v cold=%v; want primary only", hot, cold)
	}
}

func TestStoreDeleteRemovesEveryCopy(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()
	sha := r.addBlob([]byte("to be deleted"), 0)

	r.mover.fault = func(step string) error {
		if step == stepFlipped {
			return errInjectedCrash // leaves a copy on both tiers
		}

		return nil
	}

	if _, err := r.mover.Move(ctx, sha, testColdBackend, "test"); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("Move = %v", err)
	}

	if hot, cold, _ := r.where(sha); !hot || !cold {
		t.Fatalf("setup: want a copy on both tiers, hot=%v cold=%v", hot, cold)
	}

	if err := r.store.Delete(ctx, sha); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if hot, cold, _ := r.where(sha); hot || cold {
		t.Errorf("copies left after delete: hot=%v cold=%v", hot, cold)
	}

	if err := r.store.Delete(ctx, sha); err != nil {
		t.Errorf("deleting an absent blob = %v, want nil", err)
	}
}

func TestStoreListWalksEveryTier(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()

	var want []string

	for _, s := range []string{"one", "two", "three", "four", "five"} {
		want = append(want, r.addBlob([]byte("list "+s), 0))
	}

	r.moveCold(want[0])
	r.moveCold(want[1])

	var got []string

	token := ""

	for range 20 {
		ids, next, err := r.store.List(ctx, token, 2)
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		got = append(got, ids...)

		if token = next; token == "" {
			break
		}
	}

	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("List returned %v, want %v", got, want)
	}
}

func TestStoreRecordsAccessOnlyForMarkedReads(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()
	sha := r.addBlob([]byte("downloaded"), 0)

	tracker := NewAccessTracker(r.db, time.Hour, nil)
	r.store = NewStore(r.reg, r.db, tracker, nil, nil)

	read := func(ctx context.Context) {
		rc, err := r.store.Get(ctx, sha)
		if err != nil {
			t.Fatal(err)
		}

		_ = rc.Close()
	}

	read(ctx) // background read: not a download

	if tracker.Pending() != 0 {
		t.Fatalf("unmarked read was recorded")
	}

	read(storage.WithAccessTracking(ctx))

	if tracker.Pending() != 1 {
		t.Fatalf("marked read pending = %d, want 1", tracker.Pending())
	}

	if err := tracker.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	if _, ok := r.db.touched[sha]; !ok || tracker.Pending() != 0 {
		t.Errorf("flush did not persist the access (touched=%v pending=%d)", r.db.touched, tracker.Pending())
	}
}
