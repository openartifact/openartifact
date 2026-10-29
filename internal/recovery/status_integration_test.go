// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestStatusReflectsTheJournal follows the status through an outage of the
// store: a backlog that is backing off, the recorded error, then a clean state.
func TestStatusReflectsTheJournal(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := &flakyStore{BlobStore: newFSStore(t)}
	j := newTestJournal(t, db, store)

	if err := j.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	store.failing.Store(true)
	seedRaw(t, db, store.BlobStore, "raw1", "a.txt", []byte("hello"))

	if _, failed, err := j.DrainOnce(ctx); err != nil || failed == 0 {
		t.Fatalf("DrainOnce failed=%d err=%v", failed, err)
	}

	st, err := j.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !st.Enabled || st.Backlog == 0 || st.RetryingRows == 0 {
		t.Errorf("during the outage: %+v", st)
	}

	if !strings.Contains(st.LastError, "store unavailable") || st.LastErrorAt == nil {
		t.Errorf("last error = %q at %v, want the store failure recorded", st.LastError, st.LastErrorAt)
	}

	if st.TablesTotal != j.reg.Len() || st.BacklogCap == 0 || st.SchemaVersion == 0 {
		t.Errorf("static fields: %+v", st)
	}

	store.failing.Store(false)
	mustExec(t, db, `UPDATE recovery_dirty SET retry_at = now()`)
	reconcileAll(t, j)

	st, err = j.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if st.Backlog != 0 || st.RetryingRows != 0 || st.OldestDirtyAgeSeconds != 0 {
		t.Errorf("after recovery the backlog is not empty: %+v", st)
	}

	if st.SidecarsTracked == 0 || st.TablesReconciled != st.TablesTotal || st.LastReconcileAt == nil {
		t.Errorf("after a full reconcile: %+v", st)
	}
}

// TestJournalSwitchOffAndOn: changes made while the journal is off are not
// queued, and switching it on again resyncs them through a full reconcile.
func TestJournalSwitchOffAndOn(t *testing.T) {
	ctx := context.Background()
	db, store := newTestDB(t), newFSStore(t)
	j := newTestJournal(t, db, store)

	if err := j.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}

	// The migrations seed rows (the system project) before the journal is
	// switched off; they stay queued.
	const dirty = `SELECT table_name || ' ' || row_key FROM recovery_dirty ORDER BY 1`

	seeded := queryStrings(t, db, dirty)

	s := seedRaw(t, db, store, "raw1", "a.txt", []byte("hello"))

	if marked := queryStrings(t, db, dirty); !slices.Equal(marked, seeded) {
		t.Fatalf("rows were marked while the journal is off: %v, before the changes %v", marked, seeded)
	}

	if st, err := j.Status(ctx); err != nil || st.Enabled {
		t.Fatalf("status = %+v, %v; want disabled", st, err)
	}

	if err := j.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	reconcileAll(t, j)

	if trackedSha(t, j, "package_files", s.artifactID) == "" {
		t.Fatal("a row changed while the journal was off never got its sidecar")
	}
}
