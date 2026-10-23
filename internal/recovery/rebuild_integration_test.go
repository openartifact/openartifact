// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

// rowImages returns the journaled image of every row of the given tables, in a
// stable order, as the journal would write it.
func rowImages(t *testing.T, db *sql.DB, names ...string) []string {
	t.Helper()

	var out []string

	for _, name := range names {
		tbl, _ := Lookup(name)
		out = append(out, tableImages(t, db, tbl)...)
	}

	return out
}

func tableImages(t *testing.T, db *sql.DB, tbl *Table) []string {
	t.Helper()

	rows, err := db.QueryContext(context.Background(),
		`SELECT `+tbl.KeyExpr("t")+` || ' ' || (to_jsonb(t) - `+tbl.ExcludeSQL()+`)::text FROM `+tbl.Name+` t ORDER BY 1`)
	if err != nil {
		t.Fatalf("row images of %s: %v", tbl.Name, err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}

		out = append(out, tbl.Name+" "+s)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func runRebuild(t *testing.T, db *sql.DB, store storage.BlobStore, opts RebuildOptions) *Report {
	t.Helper()

	opts.Concurrency = 4

	rep, err := NewRebuilder(db, store, testLog(t), opts, Hooks{}).Run(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	return rep
}

func listAll(t *testing.T, store storage.BlobStore) []string {
	t.Helper()

	var all []string

	token := ""

	for {
		ids, next, err := store.List(context.Background(), token, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		all = append(all, ids...)

		if next == "" {
			return all
		}

		token = next
	}
}

func tableReport(rep *Report, name string) TableReport {
	for _, tr := range rep.Tables {
		if tr.Table == name {
			return tr
		}
	}

	return TableReport{Table: name}
}

// seedSource builds a populated source database and drains its journal.
func seedSource(t *testing.T, store storage.BlobStore) (*sql.DB, *Journal, seededRaw, seededRaw) {
	t.Helper()

	src := newTestDB(t)
	j := newTestJournal(t, src, store)

	a := seedRaw(t, src, store, "raw-a", "dir/a.txt", []byte("alpha"))
	b := seedRaw(t, src, store, "raw-b", "b.bin", []byte("bravo"))

	mustExec(t, src, `INSERT INTO raw_artifacts (repository_id, path, blob_id, deleted_at, deleted_by) VALUES ($1, 'trashed.txt', $2, now(), 'tester')`, a.repoID, a.blobID)
	mustExec(t, src, `UPDATE raw_artifacts SET metadata = '{"k": "v"}' WHERE id = $1`, b.artifactID)
	drain(t, j)

	return src, j, a, b
}

func TestRebuildRoundTrip(t *testing.T) {
	store := newFSStore(t)
	src, j, a, _ := seedSource(t, store)

	// A row deleted before the loss must not come back.
	dead := seedRaw(t, src, store, "raw-dead", "dead.txt", []byte("dead"))
	drain(t, j)
	mustExec(t, src, `DELETE FROM raw_artifacts WHERE id = $1`, dead.artifactID)
	mustExec(t, src, `DELETE FROM repositories WHERE id = $1`, dead.repoID)
	drain(t, j)

	tables := []string{"repositories", "blobs", "raw_artifacts"}
	want := rowImages(t, src, tables...)

	dst := newTestDB(t)

	dry := runRebuild(t, dst, store, RebuildOptions{DryRun: true})
	if dry.ExitCode() != ExitOK || tableReport(dry, "raw_artifacts").Inserted != 3 {
		t.Fatalf("dry run report = %+v", dry.Tables)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts`) + mustCount(t, dst, `SELECT count(*) FROM recovery_sidecars`); n != 0 {
		t.Fatalf("dry run wrote %d rows", n)
	}

	rep := runRebuild(t, dst, store, RebuildOptions{})
	if rep.ExitCode() != ExitOK {
		t.Fatalf("exit code %d: %+v %+v", rep.ExitCode(), rep.Failures, rep.Unrecoverable)
	}

	if got := rowImages(t, dst, tables...); !slices.Equal(got, want) {
		t.Fatalf("rebuilt rows differ\n got: %q\nwant: %q", got, want)
	}

	if !slices.Equal(rep.ReposRecovered, []string{"raw-a", "raw-b"}) {
		t.Fatalf("repositories recovered = %v", rep.ReposRecovered)
	}

	if trashed := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE deleted_at IS NOT NULL AND repository_id = $1`, a.repoID); trashed != 1 {
		t.Fatalf("trashed row restored as live (trashed=%d)", trashed)
	}

	// Idempotent: nothing new is inserted by a second run.
	again := runRebuild(t, dst, store, RebuildOptions{})
	if tr := tableReport(again, "raw_artifacts"); tr.Inserted != 0 || tr.AlreadyPresent != 3 || again.ExitCode() != ExitOK {
		t.Fatalf("second run = %+v", again.Tables)
	}

	// The restored rows are in step with their sidecars: draining the new
	// database writes and deletes nothing.
	before := listAll(t, store)

	jd := newTestJournal(t, dst, store)
	drain(t, jd)

	after := listAll(t, store)

	added := len(slices.DeleteFunc(slices.Clone(after), func(id string) bool { return slices.Contains(before, id) }))
	removed := len(slices.DeleteFunc(slices.Clone(before), func(id string) bool { return slices.Contains(after, id) }))

	if added != 0 || removed != 0 {
		t.Fatalf("draining the rebuilt database changed %d added / %d removed sidecars, want none", added, removed)
	}
}

func TestRebuildMissingBlobIsUnrecoverable(t *testing.T) {
	store := newFSStore(t)
	_, _, a, b := seedSource(t, store)

	if err := store.Delete(context.Background(), a.sha); err != nil {
		t.Fatal(err)
	}

	dst := newTestDB(t)
	rep := runRebuild(t, dst, store, RebuildOptions{})

	if rep.ExitCode() != ExitIncomplete || rep.MissingBlobCount != 1 {
		t.Fatalf("exit=%d missing=%d unrecoverable=%+v", rep.ExitCode(), rep.MissingBlobCount, rep.Unrecoverable)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE blob_id = $1`, a.blobID); n != 0 {
		t.Fatalf("rows of a blob with no bytes were inserted (%d)", n)
	}

	if n := mustCount(t, dst, `SELECT count(*) FROM raw_artifacts WHERE id = $1`, b.artifactID); n != 1 {
		t.Fatal("the intact row was not restored")
	}
}

// putSidecar stores a hand-made sidecar of the raw_artifacts row of s.
func putSidecar(t *testing.T, store storage.BlobStore, key string, at time.Time, row string, marker bool) string {
	t.Helper()

	tbl, _ := Lookup("raw_artifacts")

	data, sha, err := EncodeSidecar(tbl, key, SchemaVersion(), at, []byte(row))
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]string{"sha256": sha}
	if marker {
		meta = SidecarMetadata(tbl.Name, sha)
	}

	if _, err := store.Put(context.Background(), bytes.NewReader(data), meta); err != nil {
		t.Fatal(err)
	}

	return sha
}

func TestRebuildSupersededAndDeepAndInvalid(t *testing.T) {
	store := newFSStore(t)
	src, _, a, _ := seedSource(t, store)
	_ = src

	row := func(ct string) string {
		return `{"id":"` + a.artifactID + `","repository_id":"` + a.repoID + `","path":"dir/a.txt","content_type":"` + ct + `","metadata":{},"blob_id":"` + a.blobID + `"}`
	}

	// Two extra sidecars of the same row (as left by a crash or two writers):
	// the newest written_at wins.
	old := putSidecar(t, store, a.artifactID, time.Now().Add(-time.Hour), row("old/type"), true)
	newest := putSidecar(t, store, a.artifactID, time.Now().Add(time.Hour), row("new/type"), true)

	// A sidecar that lost its marker is only found with --deep.
	lost := putSidecar(t, store, "11111111-1111-1111-1111-111111111111", time.Now(), strings.Replace(row("x/y"), a.artifactID, "11111111-1111-1111-1111-111111111111", 1), false)

	// A marked blob whose content is not a sidecar is invalid.
	badSum := sha256.Sum256([]byte("garbage"))
	badID, err := store.Put(context.Background(), strings.NewReader("garbage"), SidecarMetadata("raw_artifacts", hex.EncodeToString(badSum[:])))
	if err != nil {
		t.Fatal(err)
	}

	plain := runRebuild(t, newTestDB(t), store, RebuildOptions{DryRun: true})
	if plain.Superseded < 2 || plain.SidecarsInvalid != 1 || plain.ExitCode() != ExitFailed {
		t.Fatalf("plain: superseded=%d invalid=%d exit=%d", plain.Superseded, plain.SidecarsInvalid, plain.ExitCode())
	}

	if n := tableReport(plain, "raw_artifacts").Found; n != 3 {
		t.Fatalf("found %d raw rows without --deep, want 3 (the unmarked sidecar must stay hidden)", n)
	}

	deep := runRebuild(t, newTestDB(t), store, RebuildOptions{DryRun: true, Deep: true})
	if n := tableReport(deep, "raw_artifacts").Found; n != 4 {
		t.Fatalf("found %d raw rows with --deep, want 4", n)
	}

	dst := newTestDB(t)

	rep := runRebuild(t, dst, store, RebuildOptions{PruneSuperseded: true})
	if got := mustString(t, dst, `SELECT content_type FROM raw_artifacts WHERE id = $1`, a.artifactID); got != "new/type" {
		t.Fatalf("content_type = %q, want the newest sidecar's", got)
	}

	if rep.Pruned < 2 {
		t.Fatalf("pruned %d, want at least 2", rep.Pruned)
	}

	for _, id := range []string{old} {
		if blobExists(t, store, id) {
			t.Fatalf("superseded sidecar %s survived --prune-superseded", id)
		}
	}

	for _, id := range []string{newest, badID, lost} {
		if !blobExists(t, store, id) {
			t.Fatalf("blob %s was deleted", id)
		}
	}
}

func TestRebuildRequiresListing(t *testing.T) {
	_, err := NewRebuilder(nil, noListStore{}, testLog(t), RebuildOptions{}, Hooks{}).Run(context.Background())
	if !errors.Is(err, ErrListUnsupported) {
		t.Fatalf("err = %v, want ErrListUnsupported", err)
	}
}

type noListStore struct{ storage.BlobStore }

func (noListStore) List(context.Context, string, int) ([]string, string, error) {
	return nil, "", storage.ErrListNotSupported
}
