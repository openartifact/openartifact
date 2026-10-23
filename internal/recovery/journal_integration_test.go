// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/storage"
)

func trackedSha(t *testing.T, j *Journal, table, key string) string {
	t.Helper()

	return mustString(t, j.db, `SELECT sidecar_sha256 FROM recovery_sidecars WHERE table_name = $1 AND row_key = $2`, table, key)
}

func blobExists(t *testing.T, s storage.BlobStore, id string) bool {
	t.Helper()

	ok, err := s.Exists(context.Background(), id)
	if err != nil {
		t.Fatalf("exists: %v", err)
	}

	return ok
}

func TestJournalLifecycle(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)
	j := newTestJournal(t, db, store)
	ctx := context.Background()

	s := seedRaw(t, db, store, "raw1", "a.txt", []byte("hello"))
	drain(t, j)

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("dirty marks left after drain = %d", n)
	}

	first := trackedSha(t, j, "raw_artifacts", s.artifactID)
	if first == "" {
		t.Fatal("raw artifact has no sidecar")
	}

	meta, err := store.GetMetadata(ctx, first)
	if err != nil || !IsSidecarMeta(meta) {
		t.Fatalf("sidecar metadata = %v, %v; want the sidecar marker", meta, err)
	}

	rc, err := store.Get(ctx, first)
	if err != nil {
		t.Fatalf("get sidecar: %v", err)
	}

	data, _ := io.ReadAll(rc)
	_ = rc.Close()

	sc, tbl, err := DecodeSidecar(first, data)
	if err != nil || tbl.Name != "raw_artifacts" || sc.Key != s.artifactID {
		t.Fatalf("decode sidecar: %v %v %v", sc, tbl, err)
	}

	// An update writes a new sidecar and deletes the superseded one.
	mustExec(t, db, `UPDATE raw_artifacts SET content_type = 'text/plain' WHERE id = $1`, s.artifactID)
	drain(t, j)

	second := trackedSha(t, j, "raw_artifacts", s.artifactID)
	if second == first || !blobExists(t, store, second) || blobExists(t, store, first) {
		t.Fatalf("after update: first=%s second=%s exists(first)=%v", first, second, blobExists(t, store, first))
	}

	if p := mustString(t, db, `SELECT pending_sha256 FROM recovery_sidecars WHERE row_key = $1`, s.artifactID); p != "" {
		t.Fatalf("pending_sha256 = %q after a clean write", p)
	}

	// Deleting the row deletes its sidecar and tracking row.
	mustExec(t, db, `DELETE FROM raw_artifacts WHERE id = $1`, s.artifactID)
	drain(t, j)

	if blobExists(t, store, second) || mustCount(t, db, `SELECT count(*) FROM recovery_sidecars WHERE row_key = $1`, s.artifactID) != 0 {
		t.Fatal("sidecar of a deleted row survived")
	}
}

func TestJournalSkipsUnchangedImage(t *testing.T) {
	db, store := newFSStoreDB(t)
	j := newTestJournal(t, db, store)
	s := seedRaw(t, db, store, "raw1", "a.txt", []byte("hello"))
	drain(t, j)

	before := trackedSha(t, j, "blobs", s.blobID)

	// last_accessed_at is excluded from the image: the update marks the row but
	// must not produce a new sidecar.
	mustExec(t, db, `UPDATE blobs SET last_accessed_at = now() WHERE id = $1`, s.blobID)

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty WHERE row_key = $1`, s.blobID); n != 1 {
		t.Fatalf("update did not mark the row (marks=%d)", n)
	}

	drain(t, j)

	if after := trackedSha(t, j, "blobs", s.blobID); after != before {
		t.Fatalf("an update of an excluded column rewrote the sidecar: %s -> %s", before, after)
	}
}

func newFSStoreDB(t *testing.T) (*sql.DB, storage.BlobStore) {
	t.Helper()

	return newTestDB(t), newFSStore(t)
}

// flakyStore fails Put while failing is set.
type flakyStore struct {
	storage.BlobStore

	failing atomic.Bool
}

func (f *flakyStore) Put(ctx context.Context, r io.Reader, meta map[string]string) (string, error) {
	if f.failing.Load() {
		return "", errors.New("store unavailable")
	}

	return f.BlobStore.Put(ctx, r, meta)
}

func TestJournalBacksOffAndRecovers(t *testing.T) {
	db := newTestDB(t)
	store := &flakyStore{BlobStore: newFSStore(t)}
	j := newTestJournal(t, db, store)

	store.failing.Store(true)

	s := seedRaw(t, db, store.BlobStore, "raw1", "a.txt", []byte("hello"))

	_, failed, err := j.DrainOnce(context.Background())
	if err != nil || failed == 0 {
		t.Fatalf("DrainOnce failed=%d err=%v; want failures", failed, err)
	}

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty WHERE attempts = 1 AND retry_at > now() AND last_error <> ''`); n == 0 {
		t.Fatal("failed marks were not put into backoff")
	}

	// Backed-off marks are not claimed again.
	if claimed, _, _ := j.DrainOnce(context.Background()); claimed != 0 {
		t.Fatalf("claimed %d marks still in backoff", claimed)
	}

	store.failing.Store(false)
	mustExec(t, db, `UPDATE recovery_dirty SET retry_at = now()`)
	drain(t, j)

	if trackedSha(t, j, "raw_artifacts", s.artifactID) == "" {
		t.Fatal("row never got its sidecar after the store recovered")
	}

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("dirty marks left = %d", n)
	}
}

func TestJournalCleansPendingBlob(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)
	j := newTestJournal(t, db, store)
	s := seedRaw(t, db, store, "raw1", "a.txt", []byte("hello"))
	drain(t, j)

	// A crash after Put but before recording leaves a blob only pending_sha256 names.
	orphan, err := store.Put(context.Background(), strings.NewReader("orphan sidecar"), map[string]string{})
	if err != nil {
		t.Fatalf("put orphan: %v", err)
	}

	mustExec(t, db, `UPDATE recovery_sidecars SET pending_sha256 = $1 WHERE row_key = $2`, orphan, s.artifactID)
	mustExec(t, db, `UPDATE raw_artifacts SET content_type = 'x/y' WHERE id = $1`, s.artifactID)
	drain(t, j)

	if blobExists(t, store, orphan) {
		t.Fatal("pending blob was not cleaned")
	}
}

func TestReconcileBackfillsAndRemovesStale(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)
	j := newTestJournal(t, db, store)
	ctx := context.Background()

	keep := seedRaw(t, db, store, "keep", "a.txt", []byte("keep"))
	gone := seedRaw(t, db, store, "gone", "b.txt", []byte("gone"))
	drain(t, j)

	goneSidecar := trackedSha(t, j, "raw_artifacts", gone.artifactID)

	// Changes made while the journal is off are invisible to the triggers.
	if err := j.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}

	mustExec(t, db, `DELETE FROM raw_artifacts WHERE id = $1`, gone.artifactID)
	late := seedRaw(t, db, store, "late", "c.txt", []byte("late"))
	mustExec(t, db, `UPDATE raw_artifacts SET content_type = 'changed/type' WHERE id = $1`, keep.artifactID)

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("disabled journal still marked %d rows", n)
	}

	if err := j.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	reconcileAll(t, j)

	if trackedSha(t, j, "raw_artifacts", late.artifactID) == "" {
		t.Fatal("row created while the journal was off was not backfilled")
	}

	if blobExists(t, store, goneSidecar) {
		t.Fatal("sidecar of a row deleted while the journal was off survived")
	}

	// The full resync after re-enabling also catches the changed row.
	sc := readTrackedSidecar(t, j, store, "raw_artifacts", keep.artifactID)
	if !strings.Contains(string(sc.Row), "changed/type") {
		t.Fatalf("changed row not re-journaled: %s", sc.Row)
	}

	st, err := j.Status(ctx)
	if err != nil || !st.Enabled || st.TablesReconciled != st.TablesTotal || st.LastReconcileAt == nil {
		t.Fatalf("status after reconcile = %+v, %v", st, err)
	}
}

func readTrackedSidecar(t *testing.T, j *Journal, store storage.BlobStore, table, key string) *Sidecar {
	t.Helper()

	id := trackedSha(t, j, table, key)

	rc, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get sidecar %s: %v", id, err)
	}

	defer func() { _ = rc.Close() }()

	data, _ := io.ReadAll(rc)

	sc, _, err := DecodeSidecar(id, data)
	if err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}

	return sc
}
