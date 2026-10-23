// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

var (
	errSnapshotTTL    = errors.New("backup lease TTL must be positive")
	errSnapshotNoPool = errors.New("backup snapshots need a database pool")
	errSnapshotID     = errors.New("database returned an unexpected snapshot id")
)

// pgxQuerier is the part of *pgxpool.Pool the pgx-native queries use. A backup
// snapshot satisfies it with a single pgx transaction (pgx.Tx), so the same
// query code runs against the shared pool or against one snapshot.
type pgxQuerier interface {
	dbsqlc.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// snapshotIDPattern is the format of pg_export_snapshot() ("00000003-0000001B-1").
// SET TRANSACTION SNAPSHOT takes a literal, not a parameter, so the id is
// checked before it is spliced into the statement.
var snapshotIDPattern = regexp.MustCompile(`^[0-9A-F]+(-[0-9A-F]+)+$`)

// snapshotMaxConns bounds the connections of a snapshot's database/sql handle.
const snapshotMaxConns = 4

// backupSnapshot is a read-only view of the database frozen at one instant,
// plus the lease that keeps the blob bytes that instant refers to from being
// deleted while it is in use.
//
// The view is a *PostgresDB whose database/sql handle and pgx queries both run
// inside REPEATABLE READ, read-only transactions that imported the snapshot
// exported by the first one, so every table read through it
// belongs to the same moment. Only the narrow SnapshotReader interface is
// handed out, so callers cannot reach the write methods the view inherits.
type backupSnapshot struct {
	*PostgresDB // the view: db and pool/q are bound to the snapshot

	parent  *PostgresDB
	leaseID string
	ttl     time.Duration

	// pgxTx is the REPEATABLE READ, read-only transaction that took and exported
	// the snapshot; the view's pgx queries run in it.
	pgxTx pgx.Tx

	heartbeatStop chan struct{}
	heartbeatDone chan struct{}
	releaseOnce   sync.Once
}

var _ SnapshotReader = (*backupSnapshot)(nil)

// BeginBackupSnapshot registers a backup lease and freezes the database. See
// SnapshotReader. The lease is renewed in the background until Release; if the
// process dies, it simply expires after leaseTTL and deferred blob deletions
// resume. The caller's ctx must outlive the backup: its cancellation ends the
// snapshot.
func (p *PostgresDB) BeginBackupSnapshot(ctx context.Context, leaseTTL time.Duration) (SnapshotReader, error) {
	if leaseTTL <= 0 {
		return nil, errSnapshotTTL
	}

	if p.rawPool == nil {
		return nil, errSnapshotNoPool
	}

	// The lease exists (blocking every pending deletion) before the snapshot is
	// taken, so no deletion committed after the snapshot can be swept before
	// the lease records which deletions the snapshot already sees.
	leaseID, err := p.q.CreateBackupLease(ctx, leaseTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("register backup lease: %w", err)
	}

	s := &backupSnapshot{parent: p, leaseID: leaseID, ttl: leaseTTL}

	if err := s.open(ctx); err != nil {
		s.Release()

		return nil, err
	}

	s.heartbeatStop = make(chan struct{})
	s.heartbeatDone = make(chan struct{})

	go s.heartbeat(context.WithoutCancel(ctx))

	return s, nil
}

// open takes the snapshot and builds the view over it.
func (s *backupSnapshot) open(ctx context.Context) error {
	p := s.parent

	tx, err := p.rawPool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin snapshot transaction: %w", err)
	}

	// This transaction is the view's pgx connection and, by staying open, keeps
	// the exported snapshot importable by the database/sql connections.
	s.pgxTx = tx

	var snapshotID, snapshot string

	// The first statement of a REPEATABLE READ transaction takes its snapshot;
	// both values describe that one snapshot.
	if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot(), pg_current_snapshot()::text`).Scan(&snapshotID, &snapshot); err != nil {
		return fmt.Errorf("export database snapshot: %w", err)
	}

	if !snapshotIDPattern.MatchString(snapshotID) {
		return errSnapshotID
	}

	if err := p.q.SetBackupLeaseSnapshot(ctx, dbsqlc.SetBackupLeaseSnapshotParams{ID: s.leaseID, Snapshot: snapshot}); err != nil {
		return fmt.Errorf("record backup lease snapshot: %w", err)
	}

	s.PostgresDB = &PostgresDB{
		db:   openSnapshotDB(p, snapshotID),
		pool: tx,
		q:    dbsqlc.New(tx),
		log:  p.log,
	}

	return nil
}

func joinSnapshotSQL(snapshotID string) string {
	return "SET TRANSACTION SNAPSHOT '" + snapshotID + "'"
}

// heartbeat keeps the lease alive until Release. A renewal that fails is
// retried at the next tick; if the database stays unreachable the lease
// expires, deletions resume, and the backup fails on the first blob that is
// gone (it never silently archives less than it should).
func (s *backupSnapshot) heartbeat(ctx context.Context) {
	defer close(s.heartbeatDone)

	t := time.NewTicker(max(s.ttl/3, time.Millisecond))
	defer t.Stop()

	for {
		select {
		case <-s.heartbeatStop:
			return
		case <-t.C:
			n, err := s.parent.q.RenewBackupLease(ctx, dbsqlc.RenewBackupLeaseParams{ID: s.leaseID, TtlSeconds: s.ttl.Seconds()})
			if err != nil {
				s.parent.log.Warnw("backup lease renewal failed", "lease", s.leaseID, "error", err)

				continue
			}

			if n == 0 {
				s.parent.log.Warnw("backup lease no longer exists; blob deletions may proceed", "lease", s.leaseID)

				return
			}
		}
	}
}

// ListBlobsAfter returns the next page of blobs rows (by sha256) as of the snapshot.
func (s *backupSnapshot) ListBlobsAfter(ctx context.Context, after string, limit int) ([]BlobRow, error) {
	rows, err := s.q.ListBlobsAfter(ctx, dbsqlc.ListBlobsAfterParams{After: after, PageSize: toInt32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list blobs after %q: %w", after, err)
	}

	out := make([]BlobRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, BlobRow{SHA256: r.Sha256, SizeBytes: r.SizeBytes})
	}

	return out, nil
}

// CountBlobs returns the number of blobs rows as of the snapshot.
func (s *backupSnapshot) CountBlobs(ctx context.Context) (int64, error) {
	n, err := s.q.CountBlobs(ctx)
	if err != nil {
		return 0, fmt.Errorf("count blobs: %w", err)
	}

	return n, nil
}

// Release ends the snapshot and removes the lease. It is idempotent and safe
// after a failed BeginBackupSnapshot.
func (s *backupSnapshot) Release() {
	s.releaseOnce.Do(func() {
		if s.heartbeatStop != nil {
			close(s.heartbeatStop)
			<-s.heartbeatDone
		}

		// Cleanup must run even when the backup's own context is already done.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if s.PostgresDB != nil {
			_ = s.db.Close()
		}

		if s.pgxTx != nil {
			_ = s.pgxTx.Rollback(ctx)
		}

		if err := s.parent.q.DeleteBackupLease(ctx, s.leaseID); err != nil {
			// The lease expires by itself; only the grace of deferred deletions is extended.
			s.parent.log.Warnw("failed to remove backup lease; it will expire", "lease", s.leaseID, "error", err)
		}
	})
}

// openSnapshotDB returns a database/sql handle whose every connection joins the
// exported snapshot in a REPEATABLE READ, read-only transaction that is never
// ended: reads through it see the snapshot, and a connection that dies is
// replaced by one that joins the same snapshot (valid until the coordinator
// transaction ends).
func openSnapshotDB(p *PostgresDB, snapshotID string) *sql.DB {
	cfg := p.rawPool.Config().ConnConfig.Copy()

	inner := stdlib.GetConnector(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, c *pgx.Conn) error {
		if _, err := c.Exec(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
			return fmt.Errorf("begin snapshot session: %w", err)
		}

		if _, err := c.Exec(ctx, joinSnapshotSQL(snapshotID)); err != nil {
			return fmt.Errorf("join snapshot: %w", err)
		}

		return nil
	}))

	db := sql.OpenDB(&snapshotConnector{inner: inner})
	db.SetMaxOpenConns(snapshotMaxConns)
	db.SetMaxIdleConns(snapshotMaxConns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	return db
}

// snapshotConnector hands out connections that stay inside their snapshot
// transaction when database/sql returns them to its pool: the pgx driver would
// otherwise discard a connection with an open transaction (ResetSession).
type snapshotConnector struct {
	inner driver.Connector
}

func (c *snapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect snapshot session: %w", err)
	}

	pgxConn, ok := conn.(*stdlib.Conn)
	if !ok {
		_ = conn.Close()

		return nil, errSnapshotID
	}

	return &snapshotConn{Conn: pgxConn}, nil
}

func (c *snapshotConnector) Driver() driver.Driver { return c.inner.Driver() }

// snapshotConn is a pgx driver connection that is kept in its transaction.
type snapshotConn struct {
	*stdlib.Conn
}

// ResetSession keeps the connection (and its snapshot transaction) in the pool.
func (c *snapshotConn) ResetSession(context.Context) error {
	if c.Conn.Conn().IsClosed() {
		return driver.ErrBadConn
	}

	return nil
}

var (
	_ driver.SessionResetter = (*snapshotConn)(nil)
	_ pgxQuerier             = (*pgxpool.Pool)(nil)
	_ pgxQuerier             = pgx.Tx(nil)
)
