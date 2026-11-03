// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package recovery

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// statementCounter counts the statements pgx sends (queries and execs, one per
// round trip), whichever pool connection sends them.
type statementCounter struct{ n atomic.Int64 }

func (c *statementCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)

	return ctx
}

func (*statementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// openCountingDB opens a second pool on the database of db whose statements
// are counted.
func openCountingDB(t *testing.T, db *sql.DB) (*sql.DB, *statementCounter) {
	t.Helper()

	name := mustString(t, db, `SELECT current_database()`)

	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	u.Path = "/" + name

	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatalf("parse pgx config: %v", err)
	}

	counter := &statementCounter{}
	cfg.Tracer = counter

	counted := stdlib.OpenDB(*cfg)
	counted.SetMaxOpenConns(4)

	t.Cleanup(func() { _ = counted.Close() })

	return counted, counter
}

// TestIdleWorkerPassStatementBound guards the idle cost of the journal: with
// nothing dirty and every table reconciled, a worker pass must not issue a
// statement per table (it did: 25 tables, an INSERT and a SELECT each, every
// 10 seconds).
func TestIdleWorkerPassStatementBound(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)
	seedRaw(t, db, store, "idle", "a.txt", []byte("idle"))

	counted, counter := openCountingDB(t, db)
	j := NewJournal(counted, store, testLog(t), nil, JournalConfig{Batch: 100, Concurrency: 4, ReconcileInterval: time.Hour})
	w := NewWorker(j, testLog(t), time.Hour)

	// Bring the journal to rest: drained, every table reconciled once.
	reconcileAll(t, j)
	w.RunPass(context.Background())
	w.RunPass(context.Background())

	const (
		passes  = 6
		perPass = 12
	)

	before := counter.n.Load()

	for range passes {
		if !w.RunPass(context.Background()) {
			t.Fatal("pass did not run")
		}
	}

	got := counter.n.Load() - before

	t.Logf("idle statements: %d over %d passes (%d tables)", got, passes, j.reg.Len())

	if got > passes*perPass {
		t.Fatalf("idle statements = %d over %d passes, want at most %d per pass (registry holds %d tables)",
			got, passes, perPass, j.reg.Len())
	}

	// Still alive: a change is picked up by the next pass.
	seedRaw(t, db, store, "idle2", "b.txt", []byte("idle2"))
	w.RunPass(context.Background())

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("dirty marks left after a pass = %d", n)
	}
}

// TestSetEnabledUnchangedWritesNothing: a boot that finds the switch already in
// the wanted position must not touch recovery_state.
func TestSetEnabledUnchangedWritesNothing(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)
	j := newTestJournal(t, db, store)
	ctx := context.Background()

	if err := j.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	xmin := mustString(t, db, `SELECT xmin::text FROM recovery_state WHERE id`)

	for range 3 {
		if err := j.SetEnabled(ctx, true); err != nil {
			t.Fatal(err)
		}
	}

	if got := mustString(t, db, `SELECT xmin::text FROM recovery_state WHERE id`); got != xmin {
		t.Fatalf("recovery_state rewritten by an unchanged SetEnabled (xmin %s -> %s)", xmin, got)
	}

	// A real flip still works and an off -> on flip still queues the full resync.
	if err := j.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}

	if mustCount(t, db, `SELECT count(*) FROM recovery_state WHERE enabled`) != 0 {
		t.Fatal("journal still enabled")
	}

	if err := j.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_reconcile WHERE full_pass`); n != j.reg.Len() {
		t.Fatalf("full resync queued for %d tables, want %d", n, j.reg.Len())
	}
}

func dirtySeq(t *testing.T, db *sql.DB, table, key string) string {
	t.Helper()

	return mustString(t, db, `SELECT seq::text FROM recovery_dirty WHERE table_name = $1 AND row_key = $2`, table, key)
}

func seqLast(t *testing.T, db *sql.DB) int {
	t.Helper()

	return mustCount(t, db, `SELECT last_value::int FROM recovery_dirty_seq`)
}

// TestMarkOncePerRowPerTransaction: repeated changes of a row inside one
// transaction queue one mark (one nextval), a change in a later transaction
// still re-marks it, and a rolled back transaction leaves nothing.
func TestMarkOncePerRowPerTransaction(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)
	j := newTestJournal(t, db, store)
	ctx := context.Background()

	s := seedRaw(t, db, store, "mark", "a.txt", []byte("mark"))
	drain(t, j)

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("dirty marks after drain = %d", n)
	}

	before := seqLast(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		if _, err := tx.ExecContext(ctx, `UPDATE package_files SET attrs = jsonb_set(attrs, '{metadata}', to_jsonb($2::int)) WHERE id = $1`,
			s.artifactID, i); err != nil {
			t.Fatal(err)
		}

		if _, err := tx.ExecContext(ctx, `UPDATE package_versions SET metadata = jsonb_build_object('n', $2::int) WHERE id = $1`,
			s.versionID, i); err != nil {
			t.Fatal(err)
		}
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 2 {
		t.Fatalf("dirty marks = %d, want 2 (file and version)", n)
	}

	if used := seqLast(t, db) - before; used != 2 {
		t.Fatalf("recovery_dirty_seq advanced by %d for 10 changes of 2 rows, want 2", used)
	}

	// The last image wins: the drained sidecar is the row as committed.
	drain(t, j)

	first := dirtySeqAfterRemark(t, db, j, s.artifactID)
	if first == "" {
		t.Fatal("a change in a later transaction did not re-mark the row")
	}

	// A rolled back change queues nothing.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE package_files SET attrs = jsonb_set(attrs, '{metadata}', '"rolled"') WHERE id = $1`,
		s.artifactID,
	); err != nil {
		t.Fatal(err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	drain(t, j)

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("rolled back transaction left %d marks", n)
	}
}

// dirtySeqAfterRemark changes the file in its own transaction twice (two
// transactions) and returns the seq of the mark when the second one bumped it.
func dirtySeqAfterRemark(t *testing.T, db *sql.DB, j *Journal, fileID string) string {
	t.Helper()

	mustExec(t, db, `UPDATE package_files SET attrs = jsonb_set(attrs, '{metadata}', '"one"') WHERE id = $1`, fileID)

	one := dirtySeq(t, db, "package_files", fileID)

	mustExec(t, db, `UPDATE package_files SET attrs = jsonb_set(attrs, '{metadata}', '"two"') WHERE id = $1`, fileID)

	two := dirtySeq(t, db, "package_files", fileID)

	if one == two {
		return ""
	}

	drain(t, j)

	return two
}

// TestMarkSurvivesRestartBeforeDrain: marks are committed with the change, so a
// process that dies before draining loses nothing: a fresh journal over the same
// database writes every sidecar, and a rebuild from them restores the rows.
func TestMarkSurvivesRestartBeforeDrain(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)

	a := seedRaw(t, db, store, "crash-a", "a.txt", []byte("alpha"))
	b := seedRaw(t, db, store, "crash-b", "b.txt", []byte("bravo"))

	marks := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`)
	if marks == 0 {
		t.Fatal("no marks queued")
	}

	// The writer "crashes" here: nothing was drained. A new process starts.
	restarted := newTestJournal(t, db, store)
	drain(t, restarted)

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("dirty marks left = %d", n)
	}

	for _, id := range []string{a.artifactID, b.artifactID} {
		if sha := trackedSha(t, restarted, "package_files", id); sha == "" || !blobExists(t, store, sha) {
			t.Fatalf("row %s has no stored sidecar after the restart", id)
		}
	}

	tables := []string{"repositories", "blobs", "packages", "package_versions", "package_files"}
	want := rowImages(t, db, tables...)

	dst := newTestDB(t)
	if rep := runRebuild(t, dst, store, RebuildOptions{}); rep.ExitCode() != ExitOK {
		t.Fatalf("rebuild exit %d: %+v", rep.ExitCode(), rep.Failures)
	}

	if got := rowImages(t, dst, tables...); !slices.Equal(got, want) {
		t.Fatalf("rows rebuilt from sidecars differ\n got: %q\nwant: %q", got, want)
	}
}

// TestConcurrentDrainRoundTrip drains a backlog spanning many chunks with the
// chunks running concurrently, then rebuilds an empty database from the result.
func TestConcurrentDrainRoundTrip(t *testing.T) {
	db, store := newTestDB(t), newFSStore(t)
	j := NewJournal(db, store, testLog(t), nil, JournalConfig{Batch: 400, Concurrency: 16})

	marks := seedBulkRaw(t, db, "bulk", 60)

	// The blobs seedBulkRaw describes: bytes "bulk<i>", so a rebuild finds them.
	for i := 1; i <= 60; i++ {
		if _, err := store.Put(context.Background(), strings.NewReader("bulk"+strconv.Itoa(i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	if marks < 4*60 {
		t.Fatalf("seeded marks = %d, want at least %d", marks, 4*60)
	}

	drain(t, j)

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_dirty`); n != 0 {
		t.Fatalf("dirty marks left = %d", n)
	}

	if n := mustCount(t, db, `SELECT count(*) FROM recovery_sidecars WHERE sidecar_sha256 <> '' AND pending_sha256 IS NULL`); n < marks {
		t.Fatalf("sidecars tracked = %d, want %d", n, marks)
	}

	tables := []string{"repositories", "packages", "package_versions", "package_files"}
	want := rowImages(t, db, tables...)

	dst := newTestDB(t)

	if rep := runRebuild(t, dst, store, RebuildOptions{}); rep.ExitCode() != ExitOK {
		t.Fatalf("rebuild exit %d: %+v", rep.ExitCode(), rep.Failures)
	}

	if got := rowImages(t, dst, tables...); !slices.Equal(got, want) {
		t.Fatalf("rebuilt %d rows, want %d", len(got), len(want))
	}
}
