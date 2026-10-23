// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func blobFile(dir, sha string) string { return filepath.Join(dir, sha[:2], sha[2:]) }

// TestMoverCrashAtEveryStep kills the move after each protocol step and checks
// the two promises: at every instant there is exactly one authoritative
// placement and its bytes exist and read back (checked on the "crashed" process
// and again on a restarted one), and recovery finishes the move.
func TestMoverCrashAtEveryStep(t *testing.T) {
	t.Parallel()

	data := []byte("crash me at every step of the protocol")

	for _, tc := range []struct {
		step string
		// placement the database holds when the process dies after step.
		placement string
		// copies on disk at that moment.
		hot, cold bool
		state     string
	}{
		{stepClaimed, "", true, false, models.TierMoveCopying},
		{stepCopied, "", true, true, models.TierMoveCopying},
		{stepVerified, "", true, true, models.TierMoveCopying},
		{stepFlipped, testColdBackend, true, true, models.TierMoveFlipped},
		// The old copy is queued for deletion, not removed: the sweeper does that.
		{stepCleaned, testColdBackend, true, true, models.TierMoveFlipped},
	} {
		t.Run(tc.step, func(t *testing.T) {
			t.Parallel()

			r := newRig(t)
			sha := r.addBlob(data, 48*3600)

			r.mover.fault = func(step string) error {
				if step == tc.step {
					return errInjectedCrash
				}

				return nil
			}

			if _, err := r.mover.Move(context.Background(), sha, testColdBackend, "test"); !errors.Is(err, errInjectedCrash) {
				t.Fatalf("Move = %v, want the injected crash", err)
			}

			hot, cold, placement := r.where(sha)
			if placement != tc.placement || hot != tc.hot || cold != tc.cold {
				t.Fatalf("after crash at %s: placement=%q hot=%v cold=%v, want placement=%q hot=%v cold=%v",
					tc.step, placement, hot, cold, tc.placement, tc.hot, tc.cold)
			}

			if (placement == "" && !hot) || (placement == testColdBackend && !cold) {
				t.Fatalf("placement %q has no bytes behind it", placement)
			}

			if row := r.db.moves[sha]; row == nil || row.State != tc.state {
				t.Fatalf("journal row = %+v, want state %s", row, tc.state)
			}

			r.mustReadable(sha, data)

			// New process: only database rows and stored bytes survive.
			r.restart()
			r.coldPolicy()
			r.mustReadable(sha, data)

			st, err := r.engine.RunPass(context.Background())
			if err != nil {
				t.Fatalf("recovery pass: %v", err)
			}

			if st.Recovered != 1 {
				t.Errorf("recovered = %d, want 1 (stats %+v)", st.Recovered, st)
			}

			r.assertRecovered(sha, data)
		})
	}
}

// assertRecovered checks the state after a recovery pass: the cold copy is
// authoritative, the old copy is queued (still there until the sweep, a running
// backup may be reading it), and the sweep leaves the cold copy only.
func (r *rig) assertRecovered(sha string, data []byte) {
	r.t.Helper()

	hot, cold, placement := r.where(sha)
	if placement != testColdBackend || !cold {
		r.t.Fatalf("after recovery: placement=%q hot=%v cold=%v, want cold placed", placement, hot, cold)
	}

	if !r.db.queued(sha, "") {
		r.t.Fatalf("old primary copy not queued for deletion: %v", r.db.deletions)
	}

	r.sweep()

	if hot, cold, _ = r.where(sha); hot || !cold {
		r.t.Fatalf("after sweep: hot=%v cold=%v, want cold only", hot, cold)
	}

	if len(r.db.moves) != 0 {
		r.t.Errorf("journal not empty after recovery: %+v", r.db.moves)
	}

	r.mustReadable(sha, data)
}

func TestMoverHappyPathBothDirections(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	data := []byte("there and back again")
	sha := r.addBlob(data, 0)
	ctx := context.Background()

	for _, step := range []struct {
		to        string
		hot, cold bool
	}{{testColdBackend, false, true}, {"", true, false}} {
		out, err := r.mover.Move(ctx, sha, step.to, "test")
		if err != nil || out != OutcomeMoved {
			t.Fatalf("Move to %q = %v, %v", step.to, out, err)
		}

		// Both copies exist until the sweeper retires the old one.
		if hot, cold, _ := r.where(sha); !hot || !cold {
			t.Fatalf("right after move to %q: hot=%v cold=%v, want both copies queued for the sweeper", step.to, hot, cold)
		}

		r.sweep()

		hot, cold, placement := r.where(sha)
		if placement != step.to || hot != step.hot || cold != step.cold {
			t.Fatalf("after move to %q: placement=%q hot=%v cold=%v", step.to, placement, hot, cold)
		}

		r.mustReadable(sha, data)
	}

	if out, err := r.mover.Move(ctx, sha, "", "test"); err != nil || out != OutcomeSkipped {
		t.Errorf("Move to the current tier = %v, %v; want skipped", out, err)
	}

	if out, err := r.mover.Move(ctx, "deadbeef", "", "test"); err != nil || out != OutcomeGone {
		t.Errorf("Move of an unknown blob = %v, %v; want gone", out, err)
	}
}

// TestMoverCorruptCopyNeverFlips damages the target copy after it was written
// (same size, other bytes). The read-back catches it, the placement stays on the
// source and the bad copy is removed so a retry starts clean.
func TestMoverCorruptCopyNeverFlips(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	data := []byte("bytes that must arrive intact")
	sha := r.addBlob(data, 0)

	corrupt := true
	r.mover.fault = func(step string) error {
		if step == stepCopied && corrupt {
			bad := make([]byte, len(data))
			copy(bad, "garbage")

			if err := os.WriteFile(blobFile(r.coldDir, sha), bad, 0o600); err != nil {
				t.Errorf("corrupt target copy: %v", err)
			}
		}

		return nil
	}

	_, err := r.mover.Move(context.Background(), sha, testColdBackend, "test")
	if !errors.Is(err, errCopyChecksum) {
		t.Fatalf("Move = %v, want a checksum error", err)
	}

	hot, cold, placement := r.where(sha)
	if placement != "" || !hot || cold {
		t.Fatalf("placement=%q hot=%v cold=%v; want hot only, bad copy removed", placement, hot, cold)
	}

	if row := r.db.moves[sha]; row == nil || row.Attempts != 1 || row.LastError == "" {
		t.Fatalf("failure not journaled: %+v", row)
	}

	r.mustReadable(sha, data)

	corrupt = false

	row := *r.db.moves[sha]
	if out, err := r.mover.Resume(context.Background(), &row); err != nil || out != OutcomeMoved {
		t.Fatalf("retry = %v, %v; want moved", out, err)
	}

	r.sweep()

	if hot, cold, placement = r.where(sha); placement != testColdBackend || hot || !cold {
		t.Fatalf("after retry: placement=%q hot=%v cold=%v", placement, hot, cold)
	}

	r.mustReadable(sha, data)
}

// TestMoverBlobChangedMidMove covers the races the flip's re-check exists for.
func TestMoverBlobChangedMidMove(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		mutate    func(r *rig, sha string)
		want      Outcome
		placement string
		gone      bool
	}{
		{
			name: "blob deleted",
			mutate: func(r *rig, sha string) {
				r.db.mu.Lock()
				delete(r.db.blobs, sha)
				r.db.mu.Unlock()
			},
			want: OutcomeGone, gone: true,
		},
		{
			name: "placement moved elsewhere",
			mutate: func(r *rig, sha string) {
				r.db.mu.Lock()
				r.db.blobs[sha].BackendID = "elsewhere"
				r.db.mu.Unlock()
			},
			want: OutcomeSkipped, placement: "elsewhere",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t)
			sha := r.addBlob([]byte("raced "+tc.name), 0)

			r.mover.fault = func(step string) error {
				if step == stepVerified {
					tc.mutate(r, sha)
				}

				return nil
			}

			out, err := r.mover.Move(context.Background(), sha, testColdBackend, "test")
			if err != nil || out != tc.want {
				t.Fatalf("Move = %v, %v; want %v", out, err, tc.want)
			}

			if _, cold, placement := r.where(sha); cold || placement != tc.placement {
				t.Errorf("cold copy left behind (%v) or placement=%q, want %q", cold, placement, tc.placement)
			}

			if len(r.db.moves) != 0 {
				t.Errorf("journal row survived: %+v", r.db.moves)
			}
		})
	}
}

// TestMoverKeepsAuthoritativeCopyWhenMovedAgain: a flipped journal row whose blob
// has since been moved back must not strip the blob of its live copy.
func TestMoverKeepsAuthoritativeCopyWhenMovedAgain(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	data := []byte("moved twice")
	sha := r.addBlob(data, 0)

	r.mover.fault = func(step string) error {
		if step == stepFlipped {
			return errInjectedCrash
		}

		return nil
	}

	if _, err := r.mover.Move(context.Background(), sha, testColdBackend, "test"); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("Move = %v", err)
	}

	// Something put the blob back on the primary store (hot copy still exists) before recovery.
	r.db.mu.Lock()
	r.db.blobs[sha].BackendID = ""
	r.db.mu.Unlock()

	r.mover.fault = nil

	row := *r.db.moves[sha]
	if _, err := r.mover.Resume(context.Background(), &row); err != nil {
		t.Fatalf("Resume = %v", err)
	}

	if hot, _, placement := r.where(sha); placement != "" || !hot {
		t.Fatalf("authoritative hot copy was removed: placement=%q hot=%v", placement, hot)
	}

	r.mustReadable(sha, data)
}

// TestMoverKeepsJournalWhenQueueingOldCopyFails: the old copy is retired through
// the deletion queue; if queueing fails the move stays journaled as flipped and
// a retry queues it, so the copy is never silently left behind.
func TestMoverKeepsJournalWhenQueueingOldCopyFails(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	ctx := context.Background()
	data := []byte("queue me later")
	sha := r.addBlob(data, 0)

	r.db.enqueueErr = errors.New("database unavailable")

	if _, err := r.mover.Move(ctx, sha, testColdBackend, "test"); err == nil {
		t.Fatal("Move succeeded although the old copy could not be queued")
	}

	row := r.db.moves[sha]
	if row == nil || row.State != models.TierMoveFlipped {
		t.Fatalf("journal row = %+v, want state flipped", row)
	}

	r.db.enqueueErr = nil

	retry := *row
	if out, err := r.mover.Resume(ctx, &retry); err != nil || out != OutcomeMoved {
		t.Fatalf("Resume = %v, %v; want moved", out, err)
	}

	if !r.db.queued(sha, "") {
		t.Errorf("old primary copy not queued after the retry: %v", r.db.deletions)
	}

	if len(r.db.moves) != 0 {
		t.Errorf("journal not empty: %+v", r.db.moves)
	}
}
