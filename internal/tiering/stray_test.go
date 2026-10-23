// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

// The tests in this file pin the guarantee documented under "Writers that race
// a move" in docs/operations/storage-tiering.md: whichever way bytes reach a
// tier, a copy that is not the blob's placement is always queued for deletion,
// so after the deletion sweeper has run no stray copy is left.

// racingStore runs `before` once, right before the first Put reaches the
// wrapped store: the window between a writer's placement read and its write.
type racingStore struct {
	storage.BlobStore

	before func()
}

func (s *racingStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	if f := s.before; f != nil {
		s.before = nil
		f()
	}

	return s.BlobStore.Put(ctx, data, metadata)
}

// raceOnPut makes the next Put on the primary tier happen after before() ran.
func (r *rig) raceOnPut(before func()) {
	r.t.Helper()

	r.replaceTier("", func(t *Tier) { t.Store = &racingStore{BlobStore: t.Store, before: before} })
}

// noStray fails unless the bytes of sha are on the placement tier only.
func (r *rig) noStray(sha, wantBackend string) {
	r.t.Helper()

	hot, cold, placement := r.where(sha)

	if placement != wantBackend {
		r.t.Fatalf("placement = %q, want %q", placement, wantBackend)
	}

	wantHot, wantCold := wantBackend == "", wantBackend == testColdBackend
	if hot != wantHot || cold != wantCold {
		r.t.Errorf("copies: hot=%v cold=%v, want hot=%v cold=%v", hot, cold, wantHot, wantCold)
	}
}

func TestPutRacingAMoveLeavesNoStrayCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("uploaded while the same blob is moved to the cold tier")

	r := newRig(t)
	sha := r.addBlob(data, 48*time.Hour)

	// The uploader read the placement (hot) and is about to write; before the
	// write lands the mover flips the blob to cold and the sweeper retires the
	// hot copy, so the write recreates it.
	r.raceOnPut(func() { r.moveCold(sha) })

	if id, err := r.store.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil || id != sha {
		t.Fatalf("Put = %q, %v", id, err)
	}

	if hot, _, _ := r.where(sha); !hot {
		t.Fatalf("setup: the racing write was meant to recreate the hot copy")
	}

	if !r.db.queued(sha, "") {
		t.Fatalf("the hot copy the racing write created was not queued for deletion: %v", r.db.deletions)
	}

	r.sweep()
	r.noStray(sha, testColdBackend)
	r.mustReadable(sha, data)
}

func TestPutWithoutShaMetadataRacingAMoveLeavesNoStrayCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("uploaded without announcing its digest")

	r := newRig(t)
	sha := r.addBlob(data, 0)
	r.moveCold(sha)

	// Without the digest the placement cannot be read before the write, so the
	// bytes land on the primary store; the id the store returns settles it.
	if id, err := r.store.Put(ctx, bytes.NewReader(data), nil); err != nil || id != sha {
		t.Fatalf("Put = %q, %v", id, err)
	}

	if !r.db.queued(sha, "") {
		t.Fatalf("the hot copy was not queued for deletion: %v", r.db.deletions)
	}

	r.sweep()
	r.noStray(sha, testColdBackend)
}

func TestPutQueuesNothingWhenNoMoveRaced(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("plain uploads leave the queue alone")

	r := newRig(t)
	sha := r.addBlob(data, 0)

	put := func(what string) {
		t.Helper()

		if _, err := r.store.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
			t.Fatalf("%s: %v", what, err)
		}

		if len(r.db.deletions) != 0 {
			t.Fatalf("%s queued deletions: %v", what, r.db.deletions)
		}
	}

	put("re-upload of a hot blob")

	r.moveCold(sha)
	put("re-upload of a cold blob")

	fresh := []byte("a blob nobody has a row for")

	if _, err := r.store.Put(ctx, bytes.NewReader(fresh), nil); err != nil {
		t.Fatalf("new blob: %v", err)
	}

	if len(r.db.deletions) != 0 {
		t.Errorf("a new blob queued deletions: %v", r.db.deletions)
	}
}

func TestPutFailsWhenItsCopyCannotBeAccounted(t *testing.T) {
	t.Parallel()

	data := []byte("an upload whose copy cannot be tracked")

	t.Run("the placement cannot be re-read after the write", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		sha := r.addBlob(data, 0)

		r.raceOnPut(func() {
			r.db.mu.Lock()
			r.db.placementErr = errors.New("connection reset")
			r.db.mu.Unlock()
		})

		if _, err := r.store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err == nil {
			t.Error("Put succeeded although the placement could not be verified")
		}
	})

	t.Run("the stray copy cannot be queued", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		sha := r.addBlob(data, 0)

		r.raceOnPut(func() {
			r.moveCold(sha)

			r.db.mu.Lock()
			r.db.enqueueErr = errors.New("queue unavailable")
			r.db.mu.Unlock()
		})

		if _, err := r.store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err == nil {
			t.Error("Put succeeded although its stray copy could not be queued")
		}
	})
}

func TestMoveQueuesCopiesOnEveryOtherTier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("left behind on a tier the move does not touch")

	r := newRig(t)
	r.addExtraTier("filesystem", map[string]any{"path": filepath.Join(t.TempDir(), "extra")})
	r.restart()

	extra, ok := r.reg.Snapshot().ByBackend(testExtraBackend)
	if !ok || extra.Store == nil {
		t.Fatalf("extra tier did not open: %+v", extra)
	}

	sha := r.addBlob(data, 0)

	// An upload that raced an earlier move left bytes on a tier that is neither
	// the placement nor the source of the next move.
	if _, err := extra.Store.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	r.moveCold(sha)

	if inExtra, _ := extra.Store.Exists(ctx, sha); inExtra {
		t.Error("the stray copy on the third tier survived the move and its sweep")
	}

	r.noStray(sha, testColdBackend)
	r.mustReadable(sha, data)
}

func TestMoveIgnoresATierThatCannotBeChecked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("moved while another tier is down")

	r := newRig(t)
	r.addExtraTier("filesystem", map[string]any{"path": filepath.Join(t.TempDir(), "extra")})
	r.restart()

	sha := r.addBlob(data, 0)

	r.replaceTier(testExtraBackend, func(c *Tier) { c.Store = failingStore{c.Store} })

	if out, err := r.mover.Move(ctx, sha, testColdBackend, "test"); err != nil || out != OutcomeMoved {
		t.Fatalf("Move = %v, %v; an unreachable unrelated tier must not stall it", out, err)
	}

	if !r.db.queued(sha, "") {
		t.Errorf("the source copy was not queued: %v", r.db.deletions)
	}

	if r.db.queued(sha, testExtraBackend) {
		t.Errorf("a copy was queued on a tier that could not be checked: %v", r.db.deletions)
	}
}

// --- presigned-upload promotion --------------------------------------------

// stagingStore is a primary store with a staging area: staged objects live in
// memory, PromoteStaging writes the blob like the S3 backend's server-side copy
// does (skipping an existing blob).
type stagingStore struct {
	storage.BlobStore

	staged map[string]stagedObject
	// before runs once, right before PromoteStaging writes (the race window).
	before func()
}

type stagedObject struct {
	data    []byte
	version string
}

func (s *stagingStore) StagingSupported() bool { return true }

func (s *stagingStore) PresignStagingPut(context.Context, string, int64, time.Duration) (storage.PresignedPut, error) {
	return storage.PresignedPut{}, storage.ErrPresignNotSupported
}

func (s *stagingStore) StatStaging(_ context.Context, id string) (storage.StagedObject, error) {
	o, ok := s.staged[id]
	if !ok {
		return storage.StagedObject{}, storage.ErrStagingNotFound
	}

	return storage.StagedObject{Size: int64(len(o.data)), Version: o.version}, nil
}

func (s *stagingStore) OpenStaging(_ context.Context, id, version string) (io.ReadCloser, error) {
	o, ok := s.staged[id]

	switch {
	case !ok:
		return nil, storage.ErrStagingNotFound
	case o.version != version:
		return nil, storage.ErrStagingChanged
	}

	return io.NopCloser(bytes.NewReader(o.data)), nil
}

func (s *stagingStore) PromoteStaging(ctx context.Context, id, sha string, version string, metadata map[string]string) error {
	if f := s.before; f != nil {
		s.before = nil
		f()
	}

	if _, exists, err := s.ExistsBySHA256(ctx, sha); err != nil || exists {
		return err
	}

	rc, err := s.OpenStaging(ctx, id, version)
	if err != nil {
		return err
	}

	defer func() { _ = rc.Close() }()

	md := map[string]string{"sha256": sha}
	maps.Copy(md, metadata)

	_, err = s.Put(ctx, rc, md)

	return err
}

func (s *stagingStore) DeleteStaging(_ context.Context, id string) error {
	delete(s.staged, id)

	return nil
}

func (s *stagingStore) WalkStaging(context.Context, time.Time, func([]string) error) error {
	return nil
}

// newStagingRig is a rig whose primary store has a staging area, and an object
// staged under id that holds data.
func newStagingRig(t *testing.T, id string, data []byte) (*rig, *stagingStore, storage.StagedObject) {
	t.Helper()

	r := newRig(t)
	st := &stagingStore{BlobStore: r.hot, staged: map[string]stagedObject{id: {data: data, version: "etag-1"}}}
	r.hot = st
	r.restart()

	obj, err := st.StatStaging(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	return r, st, obj
}

func shaOf(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

const stagingID = "9d6c2c0e-5c0e-4d0e-9d6c-2c0e5c0e4d0e"

func TestPromoteStagingOfAColdBlobDoesNotRecreateAPrimaryCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("presigned re-upload of an archived blob")

	r, _, obj := newStagingRig(t, stagingID, data)
	sha := r.addBlob(data, 0)
	r.moveCold(sha)

	if err := r.store.PromoteStaging(ctx, stagingID, sha, obj.Version, map[string]string{"format": "raw"}); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}

	if len(r.db.deletions) != 0 {
		t.Errorf("a promotion that wrote nothing queued deletions: %v", r.db.deletions)
	}

	r.noStray(sha, testColdBackend)
	r.mustReadable(sha, data)
}

func TestPromoteStagingRestoresALostColdCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("the cold copy went missing")

	r, _, obj := newStagingRig(t, stagingID, data)
	sha := r.addBlob(data, 0)
	r.moveCold(sha)

	if err := r.cold.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}

	if err := r.store.PromoteStaging(ctx, stagingID, sha, obj.Version, map[string]string{"format": "raw"}); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}

	r.noStray(sha, testColdBackend) // the staged bytes went to the placement tier, not the primary store
	r.mustReadable(sha, data)
}

func TestPromoteStagingOfAColdBlobRefusesAChangedObject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("overwritten through the still-valid upload URL")

	r, _, _ := newStagingRig(t, stagingID, data)
	sha := r.addBlob(data, 0)
	r.moveCold(sha)

	if err := r.cold.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}

	if err := r.store.PromoteStaging(ctx, stagingID, sha, "etag-0", nil); !errors.Is(err, storage.ErrStagingChanged) {
		t.Fatalf("PromoteStaging = %v, want ErrStagingChanged", err)
	}

	if onCold, _ := r.cold.Exists(ctx, sha); onCold {
		t.Error("unverified bytes were stored on the cold tier")
	}
}

func TestPromoteStagingOfANewBlobLandsOnThePrimaryStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("a first upload through a presigned URL")
	sha := shaOf(data)

	r, _, obj := newStagingRig(t, stagingID, data)

	if err := r.store.PromoteStaging(ctx, stagingID, sha, obj.Version, nil); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}

	if hot, cold, _ := r.where(sha); !hot || cold {
		t.Errorf("hot=%v cold=%v, want the primary store only", hot, cold)
	}

	if len(r.db.deletions) != 0 {
		t.Errorf("a new blob queued deletions: %v", r.db.deletions)
	}
}

func TestPromoteStagingRacingAMoveLeavesNoStrayCopy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	data := []byte("promoted while the blob is moved to the cold tier")

	r, st, obj := newStagingRig(t, stagingID, data)
	sha := r.addBlob(data, 0)

	// The promotion read the placement (hot) and is about to copy; before the
	// copy lands the blob is moved and its hot copy swept, so the copy
	// recreates it.
	st.before = func() { r.moveCold(sha) }

	if err := r.store.PromoteStaging(ctx, stagingID, sha, obj.Version, nil); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}

	if hot, _, _ := r.where(sha); !hot {
		t.Fatalf("setup: the racing promotion was meant to recreate the hot copy")
	}

	if !r.db.queued(sha, "") {
		t.Fatalf("the hot copy the promotion created was not queued for deletion: %v", r.db.deletions)
	}

	r.sweep()
	r.noStray(sha, testColdBackend)
	r.mustReadable(sha, data)
}
