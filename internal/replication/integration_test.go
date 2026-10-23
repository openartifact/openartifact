// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package replication

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
	"github.com/openartifact/openartifact/pkg/logger"
)

// newReplicationTestDB creates an empty database on the TEST_DATABASE_URL
// server, applies the embedded migrations and returns a PostgresDB over it.
func newReplicationTestDB(t *testing.T) *database.PostgresDB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	dbName := fmt.Sprintf("oa_replication_%d", time.Now().UnixNano())

	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: dbName, SSLMode: sslmode, MaxOpenConns: 20, MaxIdleConns: 20,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	return pg
}

func hexID(n int) string { return fmt.Sprintf("%064x", n) }

// queueEnv runs the queue scenarios in order against one database: each
// builds on the rows the previous one left.
type queueEnv struct {
	t    *testing.T
	pg   *database.PostgresDB
	ctx  context.Context //nolint:containedctx // test fixture
	a, b string
}

func (q *queueEnv) must(err error) {
	q.t.Helper()

	if err != nil {
		q.t.Fatal(err)
	}
}

func (q *queueEnv) latestOperationWins(t *testing.T) {
	t.Helper()

	q.must(q.pg.EnqueueBlobReplication(q.ctx, q.a, database.BlobReplicationCopy))

	first, err := q.pg.GetBlobReplication(q.ctx, q.a)
	q.must(err)

	q.must(q.pg.EnqueueBlobReplication(q.ctx, q.a, database.BlobReplicationCopy))

	same, _ := q.pg.GetBlobReplication(q.ctx, q.a)
	if same.Version != first.Version {
		t.Fatalf("version changed on a repeated op: %d -> %d", first.Version, same.Version)
	}

	q.must(q.pg.EnqueueBlobReplication(q.ctx, q.a, database.BlobReplicationDelete))

	del, _ := q.pg.GetBlobReplication(q.ctx, q.a)
	if del.Op != database.BlobReplicationDelete || del.Version == first.Version {
		t.Fatalf("after a delete: %+v", del)
	}
}

func (q *queueEnv) claimsLeaseTheRow(t *testing.T) {
	t.Helper()

	got, err := q.pg.ClaimBlobReplications(q.ctx, time.Now(), 10, time.Minute)
	q.must(err)

	if len(got) != 1 || got[0].SHA256 != q.a || got[0].Op != database.BlobReplicationDelete {
		t.Fatalf("claim = %+v", got)
	}

	if again, _ := q.pg.ClaimBlobReplications(q.ctx, time.Now(), 10, time.Minute); len(again) != 0 {
		t.Fatalf("a leased row was handed out twice: %+v", again)
	}

	if expired, _ := q.pg.ClaimBlobReplications(q.ctx, time.Now().Add(2*time.Minute), 10, time.Minute); len(expired) != 1 {
		t.Fatalf("an expired lease was not reclaimed: %+v", expired)
	}
}

func (q *queueEnv) staleVersionsAreIgnored(t *testing.T) {
	t.Helper()

	cur, _ := q.pg.GetBlobReplication(q.ctx, q.a)

	// The operation changes while a worker holds the old version.
	q.must(q.pg.EnqueueBlobReplication(q.ctx, q.a, database.BlobReplicationCopy))

	if ok, err := q.pg.CompleteBlobReplication(q.ctx, q.a, cur.Version); err != nil || ok {
		t.Fatalf("stale complete = %v, %v", ok, err)
	}

	if ok, err := q.pg.FailBlobReplication(q.ctx, q.a, cur.Version, "late", time.Now()); err != nil || ok {
		t.Fatalf("stale fail = %v, %v", ok, err)
	}

	fresh, _ := q.pg.GetBlobReplication(q.ctx, q.a)
	if fresh == nil || fresh.Op != database.BlobReplicationCopy || fresh.Attempts != 0 {
		t.Fatalf("the newer operation was touched: %+v", fresh)
	}
}

func (q *queueEnv) failureBacksOff(t *testing.T) {
	t.Helper()

	fresh, _ := q.pg.GetBlobReplication(q.ctx, q.a)
	retryAt := time.Now().Add(10 * time.Minute)

	ok, err := q.pg.FailBlobReplication(q.ctx, q.a, fresh.Version, "replica down", retryAt)
	if err != nil || !ok {
		t.Fatalf("fail = %v, %v", ok, err)
	}

	failed, _ := q.pg.GetBlobReplication(q.ctx, q.a)
	if failed.Attempts != 1 || failed.LastError != "replica down" || failed.NextAttemptAt.Before(retryAt.Add(-time.Second)) {
		t.Fatalf("failed row = %+v", failed)
	}

	if due, _ := q.pg.ClaimBlobReplications(q.ctx, time.Now().Add(5*time.Minute), 10, time.Second); len(due) != 0 {
		t.Fatalf("a row in backoff was claimed: %+v", due)
	}

	due, _ := q.pg.ClaimBlobReplications(q.ctx, retryAt.Add(time.Second), 10, time.Second)
	if len(due) != 1 {
		t.Fatalf("a row past its backoff was not claimed: %+v", due)
	}

	if ok, err := q.pg.CompleteBlobReplication(q.ctx, q.a, due[0].Version); err != nil || !ok {
		t.Fatalf("complete = %v, %v", ok, err)
	}

	if gone, _ := q.pg.GetBlobReplication(q.ctx, q.a); gone != nil {
		t.Fatalf("completed row still queued: %+v", gone)
	}
}

func (q *queueEnv) releaseGivesTheRowBack(t *testing.T) {
	t.Helper()

	q.must(q.pg.EnqueueBlobReplication(q.ctx, q.b, database.BlobReplicationCopy))

	got, _ := q.pg.ClaimBlobReplications(q.ctx, time.Now(), 10, time.Hour)
	if len(got) != 1 {
		t.Fatalf("claim = %+v", got)
	}

	q.must(q.pg.ReleaseBlobReplication(q.ctx, q.b, got[0].Version))

	if back, _ := q.pg.ClaimBlobReplications(q.ctx, time.Now(), 10, time.Hour); len(back) != 1 {
		t.Fatalf("released row was not claimable: %+v", back)
	}
}

func (q *queueEnv) backlogCounts(t *testing.T) {
	t.Helper()

	q.must(q.pg.EnqueueBlobReplication(q.ctx, hexID(3), database.BlobReplicationDelete))

	cur, _ := q.pg.GetBlobReplication(q.ctx, q.b)
	_, err := q.pg.FailBlobReplication(q.ctx, q.b, cur.Version, "x", time.Now())
	q.must(err)

	bl, err := q.pg.BlobReplicationBacklog(q.ctx, 1)
	q.must(err)

	if bl.Copy != 1 || bl.Delete != 1 || bl.Failing != 1 || bl.OldestAgeSeconds < 0 {
		t.Fatalf("backlog = %+v", bl)
	}
}

func TestIntegrationQueueSemantics(t *testing.T) {
	q := &queueEnv{t: t, pg: newReplicationTestDB(t), ctx: context.Background(), a: hexID(1), b: hexID(2)}

	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"latest operation wins and the same one is a no-op", q.latestOperationWins},
		{"claims lease the row until it expires", q.claimsLeaseTheRow},
		{"complete and fail act only on the claimed version", q.staleVersionsAreIgnored},
		{"a failure backs the row off", q.failureBacksOff},
		{"release gives a claimed row back", q.releaseGivesTheRowBack},
		{"backlog counts by operation and failures", q.backlogCounts},
	} {
		t.Run(step.name, step.run)
	}
}

func TestIntegrationConcurrentClaimersGetDisjointRows(t *testing.T) {
	pg := newReplicationTestDB(t)
	ctx := context.Background()

	const total = 60

	for i := range total {
		if err := pg.EnqueueBlobReplication(ctx, hexID(100+i), database.BlobReplicationCopy); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu   sync.Mutex
		seen []string
		wg   sync.WaitGroup
	)

	for range 6 {
		wg.Go(func() {
			for {
				rows, err := pg.ClaimBlobReplications(ctx, time.Now(), 7, time.Hour)
				if err != nil {
					t.Error(err)

					return
				}

				if len(rows) == 0 {
					return
				}

				mu.Lock()
				for _, r := range rows {
					seen = append(seen, r.SHA256)
				}
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	if len(seen) != total {
		t.Fatalf("claimed %d rows, want %d", len(seen), total)
	}

	slices.Sort(seen)

	if len(slices.Compact(seen)) != total {
		t.Fatal("a row was handed to two claimers")
	}
}

type verifyStep struct {
	name string
	do   func() (bool, error)
	want bool
}

func verifyStateSteps(ctx context.Context, pg *database.PostgresDB) []verifyStep {
	return []verifyStep{
		{"request", func() (bool, error) { return pg.RequestReplicationVerify(ctx) }, true},
		{"request again while requested", func() (bool, error) { return pg.RequestReplicationVerify(ctx) }, false},
		{"save without a run", func() (bool, error) { return pg.SaveReplicationVerifyProgress(ctx, "x", json.RawMessage(`{}`)) }, false},
		{"start", func() (bool, error) { return pg.StartReplicationVerify(ctx, json.RawMessage(`{"checked":0}`)) }, true},
		{"second replica cannot start", func() (bool, error) { return pg.StartReplicationVerify(ctx, json.RawMessage(`{}`)) }, false},
		{"request while running", func() (bool, error) { return pg.RequestReplicationVerify(ctx) }, false},
		{"save", func() (bool, error) {
			return pg.SaveReplicationVerifyProgress(ctx, hexID(9), json.RawMessage(`{"checked":5}`))
		}, true},
	}
}

func hasRun(raw json.RawMessage) bool { return len(raw) != 0 && string(raw) != "null" }

func TestIntegrationVerifyStateMachine(t *testing.T) {
	pg := newReplicationTestDB(t)
	ctx := context.Background()

	st, err := pg.GetBlobReplicationState(ctx)
	if err != nil || st.VerifyRequested || hasRun(st.VerifyRun) {
		t.Fatalf("initial state = %+v, %v", st, err)
	}

	for _, s := range verifyStateSteps(ctx, pg) {
		got, err := s.do()
		if err != nil || got != s.want {
			t.Fatalf("%s = %v, %v; want %v", s.name, got, err, s.want)
		}
	}

	st, _ = pg.GetBlobReplicationState(ctx)
	if st.VerifyCursor != hexID(9) || !bytes.Contains(st.VerifyRun, []byte(`"checked"`)) {
		t.Fatalf("running state = %+v", st)
	}

	if err := pg.FinishReplicationVerify(ctx, json.RawMessage(`{"checked":9}`)); err != nil {
		t.Fatal(err)
	}

	st, _ = pg.GetBlobReplicationState(ctx)
	if st.VerifyCursor != "" || hasRun(st.VerifyRun) || !bytes.Contains(st.LastVerify, []byte(`9`)) {
		t.Fatalf("finished state = %+v", st)
	}

	if ok, err := pg.RequestReplicationVerify(ctx); err != nil || !ok {
		t.Fatalf("request after a finished pass = %v, %v", ok, err)
	}
}

func TestIntegrationProgressAndLastError(t *testing.T) {
	pg := newReplicationTestDB(t)
	ctx := context.Background()

	if err := pg.AddBlobReplicationProgress(ctx, 3, 300); err != nil {
		t.Fatal(err)
	}

	if err := pg.SetBlobReplicationLastError(ctx, "boom"); err != nil {
		t.Fatal(err)
	}

	st, _ := pg.GetBlobReplicationState(ctx)
	if st.ReplicatedTotal != 3 || st.ReplicatedBytes != 300 || st.LastError != "boom" || st.LastErrorAt == nil {
		t.Fatalf("totals = %+v", st)
	}
}

func TestIntegrationListAndReferenced(t *testing.T) {
	pg := newReplicationTestDB(t)
	ctx := context.Background()

	for _, id := range []string{hexID(5), hexID(7), hexID(6)} {
		insertBlobRow(t, pg, id)
	}

	got, err := pg.ListBlobSHAs(ctx, "", 2)
	if err != nil || !slices.Equal(got, []string{hexID(5), hexID(6)}) {
		t.Fatalf("first page = %v, %v", got, err)
	}

	got, err = pg.ListBlobSHAs(ctx, hexID(6), 2)
	if err != nil || !slices.Equal(got, []string{hexID(7)}) {
		t.Fatalf("second page = %v, %v", got, err)
	}

	if ok, err := pg.BlobReplicationReferenced(ctx, hexID(5)); err != nil || !ok {
		t.Fatalf("referenced = %v, %v", ok, err)
	}

	if ok, err := pg.BlobReplicationReferenced(ctx, hexID(99)); err != nil || ok {
		t.Fatalf("unknown blob referenced = %v, %v", ok, err)
	}
}

func insertBlobRow(t *testing.T, pg *database.PostgresDB, sha string) {
	t.Helper()

	if _, err := pg.GetDB().ExecContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1::varchar, 1, $1::text)`, sha); err != nil {
		t.Fatalf("insert blob row: %v", err)
	}
}

// e2e is a Service over the real queue and locks with two filesystem stores.
type e2e struct {
	t       *testing.T
	pg      *database.PostgresDB
	primary *faultStore
	replica *faultStore
	svc     *Service
	store   *Store
	m       *metrics.ReplicationMetrics
}

func newE2E(t *testing.T) *e2e {
	t.Helper()

	pg := newReplicationTestDB(t)
	e := &e2e{t: t, pg: pg, primary: newFS(t), replica: newFS(t), m: metrics.NewReplicationMetrics()}

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	opts := defaultTestOptions()
	opts.Interval = 30 * time.Millisecond

	svc, err := New(Config{
		Primary: e.primary, Replica: e.replica, Queue: pg, SQLDB: pg.GetDB(),
		Lock: func(ctx context.Context, sha string, fn func(ctx context.Context) error) error {
			return storage.WithBlobLock(ctx, pg, sha, fn)
		},
		Info: ReplicaInfo{Type: "filesystem", Location: "replica"}, Options: opts, Metrics: e.m, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}

	svc.core.jitter = func(time.Duration) time.Duration { return 20 * time.Millisecond }
	svc.verifyPoll = 30 * time.Millisecond
	e.svc, e.store = svc, svc.Store()

	svc.Start()
	t.Cleanup(svc.Stop)

	return e
}

func (e *e2e) upload(content string) string {
	e.t.Helper()

	id, err := e.store.Put(context.Background(), bytes.NewReader([]byte(content)), nil)
	if err != nil {
		e.t.Fatalf("put: %v", err)
	}

	insertBlobRow(e.t, e.pg, id)

	return id
}

func (e *e2e) status() Status {
	e.t.Helper()

	st, err := e.svc.Status(context.Background())
	if err != nil {
		e.t.Fatalf("status: %v", err)
	}

	return st
}

func (e *e2e) awaitVerify(since time.Time) *VerifyResult {
	e.t.Helper()

	var res *VerifyResult

	eventually(e.t, "a verify pass to finish", func() bool {
		st := e.status()
		if st.LastVerify != nil && st.LastVerify.FinishedAt != nil && st.LastVerify.FinishedAt.After(since) {
			res = st.LastVerify

			return true
		}

		return false
	})

	return res
}

func TestIntegrationReplicatesRepairsAndDeletes(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()

	id := e.upload("the artifact bytes")

	eventually(t, "the blob to reach the replica", func() bool { return has(t, e.replica, id) })

	eventually(t, "the queue to drain", func() bool {
		st := e.status()

		return st.BacklogCopy == 0 && st.ReplicatedTotal >= 1
	})

	if st := e.status(); !st.Enabled || st.Replica.Type != "filesystem" || st.ReplicatedBytes < int64(len("the artifact bytes")) {
		t.Fatalf("status = %+v", st)
	}

	// The primary loses the blob: reads are served from the replica and the
	// read repair puts it back.
	damageRemove(t, e.primary, id)

	if got := readAll(t, e.store, id); got != "the artifact bytes" {
		t.Fatalf("fallback read = %q", got)
	}

	eventually(t, "read repair to restore the primary", func() bool { return has(t, e.primary, id) })

	// A verify pass restores what read repair did not see.
	damageRemove(t, e.primary, id)

	since := time.Now()

	if ok, err := e.svc.RequestVerify(ctx); err != nil || !ok {
		t.Fatalf("request verify = %v, %v", ok, err)
	}

	res := e.awaitVerify(since)
	if res.MissingPrimary < 1 || res.Repaired < 1 || !has(t, e.primary, id) {
		t.Fatalf("verify = %+v, primary has blob: %v", res, has(t, e.primary, id))
	}

	// Deleting the last reference removes the replica copy once the sweeper runs.
	if _, err := e.pg.GetDB().ExecContext(ctx, `DELETE FROM blobs WHERE sha256 = $1`, id); err != nil {
		t.Fatal(err)
	}

	if err := storage.SafeDeleteBlob(ctx, e.pg, e.store, id); err != nil {
		t.Fatalf("SafeDeleteBlob: %v", err)
	}

	log, _ := logger.New("error", "console", "stderr")
	if _, err := worker.NewBlobDeletionSweeper(e.pg, e.store, log, 0, 100).RunOnce(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	eventually(t, "the copy on both sides to be removed", func() bool {
		return !has(t, e.primary, id) && !has(t, e.replica, id)
	})
}

// A disaster-recovery sidecar has no blobs row; the verify pass must still
// restore it to a refilled primary, while an unreferenced blob that exists
// only on the replica stays where it is.
func TestIntegrationVerifyRestoresJournalSidecars(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()

	sidecar, err := e.store.Put(ctx, bytes.NewReader([]byte(`{"table":"raw_artifacts"}`)), nil)
	if err != nil {
		t.Fatal(err)
	}

	stray, err := e.store.Put(ctx, bytes.NewReader([]byte("nothing references this")), nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.pg.GetDB().ExecContext(ctx,
		`INSERT INTO recovery_sidecars (table_name, row_key, row_hash, sidecar_sha256) VALUES ('raw_artifacts', 'k1', 'h1', $1)`, sidecar); err != nil {
		t.Fatalf("insert sidecar tracking row: %v", err)
	}

	eventually(t, "both blobs to reach the replica", func() bool {
		return has(t, e.replica, sidecar) && has(t, e.replica, stray) && e.status().BacklogCopy == 0
	})

	damageRemove(t, e.primary, sidecar)
	damageRemove(t, e.primary, stray)

	since := time.Now()

	if ok, err := e.svc.RequestVerify(ctx); err != nil || !ok {
		t.Fatalf("request verify = %v, %v", ok, err)
	}

	res := e.awaitVerify(since)
	if !has(t, e.primary, sidecar) {
		t.Fatalf("the sidecar was not restored to the primary: verify = %+v", res)
	}

	if has(t, e.primary, stray) || !has(t, e.replica, stray) {
		t.Fatalf("an unreferenced blob must stay on the replica only: verify = %+v", res)
	}
}

func TestIntegrationBacklogRetriesAndRecovers(t *testing.T) {
	e := newE2E(t)

	e.replica.down.Store(true)

	id := e.upload("written while the replica is down")

	eventually(t, "attempts to be recorded", func() bool {
		row, err := e.pg.GetBlobReplication(context.Background(), id)

		return err == nil && row != nil && row.Attempts >= 2 && row.LastError != ""
	})

	st := e.status()
	if st.BacklogCopy != 1 || st.LastError == "" {
		t.Fatalf("status during the outage = %+v", st)
	}

	// Clients are not affected: the primary still serves the blob.
	if got := readAll(t, e.store, id); got != "written while the replica is down" {
		t.Fatalf("read = %q", got)
	}

	e.replica.down.Store(false)

	eventually(t, "the backlog to clear after recovery", func() bool {
		return has(t, e.replica, id) && e.status().BacklogCopy == 0
	})
}

func damageRemove(t *testing.T, s *faultStore, id string) {
	t.Helper()

	if err := os.Remove(s.file(id)); err != nil {
		t.Fatalf("remove %s: %v", id, err)
	}
}
