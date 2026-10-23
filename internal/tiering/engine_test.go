// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

const day = 24 * time.Hour

// snapshotState captures everything a dry run must leave untouched.
type snapshotState struct {
	blobs map[string]models.BlobFacts
	moves int
	hot   map[string]bool
	cold  map[string]bool
}

func (r *rig) state(shas ...string) snapshotState {
	r.t.Helper()

	s := snapshotState{blobs: map[string]models.BlobFacts{}, hot: map[string]bool{}, cold: map[string]bool{}}

	r.db.mu.Lock()
	for sha, b := range r.db.blobs {
		s.blobs[sha] = *b
	}

	s.moves = len(r.db.moves)
	r.db.mu.Unlock()

	for _, sha := range shas {
		s.hot[sha], s.cold[sha], _ = r.where(sha)
	}

	return s
}

func TestPassMovesOnlyWhatPoliciesSelect(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.coldPolicy() // older than one day -> cold

	old := []byte("an old release")
	fresh := []byte("a fresh snapshot")
	oldSha := r.addBlob(old, 40*day)
	freshSha := r.addBlob(fresh, time.Hour)

	st, err := r.engine.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}

	if st.Moved != 1 || st.Failed != 0 || st.Bytes != int64(len(old)) {
		t.Errorf("stats = %+v, want exactly the old blob moved", st)
	}

	r.sweep()

	if hot, cold, placement := r.where(oldSha); placement != testColdBackend || hot || !cold {
		t.Errorf("old blob: placement=%q hot=%v cold=%v", placement, hot, cold)
	}

	if hot, cold, placement := r.where(freshSha); placement != "" || !hot || cold {
		t.Errorf("fresh blob: placement=%q hot=%v cold=%v", placement, hot, cold)
	}

	r.mustReadable(oldSha, old)
	r.mustReadable(freshSha, fresh)

	// A second pass has nothing left to do.
	if st, err = r.engine.RunPass(context.Background()); err != nil || st.Moved != 0 {
		t.Errorf("second pass = %+v, %v; want a no-op", st, err)
	}
}

func TestPassWithoutColdTierDoesNothing(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.coldPolicy()
	r.db.tiers = r.db.tiers[:1] // only the primary tier remains

	sha := r.addBlob([]byte("stays"), 40*day)

	if err := r.reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	if st, err := r.engine.RunPass(context.Background()); err != nil || st.Moved != 0 {
		t.Fatalf("RunPass = %+v, %v", st, err)
	}

	if _, _, placement := r.where(sha); placement != "" {
		t.Errorf("blob moved to %q without a cold tier", placement)
	}
}

// TestPassBatchBudgetResumes: a pass moves at most Batch blobs and the next one
// continues from where it stopped, until every candidate has moved.
func TestPassBatchBudgetResumes(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.coldPolicy()
	r.engine.cfg.Batch = 2

	var shas []string
	for i := range 5 {
		shas = append(shas, r.addBlob([]byte(fmt.Sprintf("blob number %d", i)), 10*day))
	}

	for pass, wantMoved := range []int{2, 2, 1, 0} {
		st, err := r.engine.RunPass(context.Background())
		if err != nil || st.Moved != wantMoved {
			t.Fatalf("pass %d = %+v, %v; want %d moved", pass, st, err, wantMoved)
		}
	}

	for _, sha := range shas {
		if _, cold, placement := r.where(sha); placement != testColdBackend || !cold {
			t.Errorf("blob %s not on cold: placement=%q cold=%v", sha[:8], placement, cold)
		}
	}
}

func TestPassPolicyByFormatNeedsRefs(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.db.policies = []*models.StoragePolicy{{
		ID: "p", Name: "releases", Enabled: true, TargetTierID: testColdTierID,
		MinAgeSeconds: new(int64(86400)), RepoGlob: "releases", Formats: []string{"maven"},
	}}

	rel := r.addBlob([]byte("release jar"), 10*day)
	other := r.addBlob([]byte("snapshot jar"), 10*day)
	unref := r.addBlob([]byte("referenced by nothing"), 10*day)
	r.db.refs[rel] = []models.BlobRef{{Repo: "releases", Format: "maven", Path: "a/b.jar"}}
	r.db.refs[other] = []models.BlobRef{{Repo: "snapshots", Format: "maven", Path: "a/c.jar"}}

	if _, err := r.engine.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}

	for sha, wantCold := range map[string]bool{rel: true, other: false, unref: false} {
		if _, cold, _ := r.where(sha); cold != wantCold {
			t.Errorf("blob %s on cold = %v, want %v", sha[:8], cold, wantCold)
		}
	}
}

func TestPromoteOnRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		promote     bool
		hotPolicy   bool // a policy that would send the blob straight back
		readAfter   bool // read after landing on cold
		wantPromote bool
	}{
		{"read cold blob is promoted", true, false, true, true},
		{"promotion disabled", false, false, true, false},
		{"never read since move", true, false, false, false},
		{"policy would move it back", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRig(t)
			r.db.tiers[1].PromoteOnRead = tc.promote

			if tc.hotPolicy {
				r.coldPolicy()
			}

			data := []byte("promote " + tc.name)
			sha := r.addBlob(data, 10*day)

			if out, err := r.mover.Move(context.Background(), sha, testColdBackend, "test"); err != nil || out != OutcomeMoved {
				t.Fatalf("seed move = %v, %v", out, err)
			}

			if tc.readAfter {
				at := time.Now().Add(time.Hour)
				r.db.mu.Lock()
				r.db.blobs[sha].LastAccessedAt = &at
				r.db.mu.Unlock()
			}

			if err := r.reg.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}

			st, err := r.engine.RunPass(context.Background())
			if err != nil {
				t.Fatalf("RunPass: %v", err)
			}

			hot, _, placement := r.where(sha)
			if got := placement == "" && hot; got != tc.wantPromote {
				t.Errorf("promoted = %v, want %v (stats %+v, placement %q)", got, tc.wantPromote, st, placement)
			}

			if tc.wantPromote && st.Promoted != 1 {
				t.Errorf("stats.Promoted = %d, want 1", st.Promoted)
			}

			r.mustReadable(sha, data)
		})
	}
}

func TestPlanReportsAndChangesNothing(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.coldPolicy()

	a := r.addBlob([]byte("old and big old and big"), 30*day)
	b := r.addBlob([]byte("old"), 30*day)
	c := r.addBlob([]byte("new"), time.Hour)

	before := r.state(a, b, c)

	plan, err := r.engine.Plan(context.Background(), nil, 10)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.Count != 2 || plan.Bytes != int64(len("old and big old and big")+len("old")) || plan.Truncated {
		t.Errorf("plan = %+v, want 2 blobs / %d bytes", plan, len("old and big old and big")+len("old"))
	}

	if len(plan.Routes) != 1 || plan.Routes[0].From != "hot" || plan.Routes[0].To != "cold" || plan.Routes[0].Count != 2 {
		t.Errorf("routes = %+v", plan.Routes)
	}

	if len(plan.Policies) != 1 || plan.Policies[0].Policy != "archive" || plan.Policies[0].Count != 2 {
		t.Errorf("policies = %+v", plan.Policies)
	}

	if len(plan.Sample) != 2 {
		t.Errorf("sample = %+v, want both blobs", plan.Sample)
	}

	if after := r.state(a, b, c); !reflect.DeepEqual(before, after) {
		t.Errorf("dry run changed state:\nbefore %+v\nafter  %+v", before, after)
	}

	if onCold, _ := r.cold.Exists(context.Background(), a); onCold {
		t.Error("dry run wrote to the cold store")
	}
}

func TestPlanSampleLimitAndDraft(t *testing.T) {
	t.Parallel()

	r := newRig(t) // no saved policies

	for i := range 6 {
		r.addBlob([]byte(fmt.Sprintf("draft blob %d", i)), 20*day)
	}

	draft := mustPolicy(t, models.StoragePolicy{ID: "d", Name: "draft", TargetTierID: testColdTierID, MinAgeSeconds: new(int64(86400))})

	plan, err := r.engine.Plan(context.Background(), draft, 3)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.Count != 6 || len(plan.Sample) != 3 {
		t.Errorf("count=%d sample=%d, want 6 counted and 3 sampled", plan.Count, len(plan.Sample))
	}

	if none, err := r.engine.Plan(context.Background(), nil, 3); err != nil || none.Count != 0 {
		t.Errorf("without policies Plan = %+v, %v; want nothing to move", none, err)
	}
}

func TestRunPassReportsFinishTime(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	st, err := r.engine.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}

	if st.StartedAt.IsZero() || st.FinishedAt.IsZero() || st.FinishedAt.Before(st.StartedAt) {
		t.Errorf("pass times = %v .. %v, want both set and ordered", st.StartedAt, st.FinishedAt)
	}
}
