// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/openartifact/openartifact/migrations"
)

var ErrChecksumDrift = errors.New("checksum drift")

const advisoryLockKey = `hashtext('openartifact-migrations')::bigint`

const createTrackingTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    VARCHAR(255) PRIMARY KEY,
	applied_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
	checksum   TEXT         NOT NULL
)`

// Migrator applies embedded SQL migrations to a PostgreSQL database.
type Migrator struct {
	db *sql.DB
	fs fs.ReadDirFS
}

// MigrationStatus describes the state of a single migration file.
type MigrationStatus struct {
	Version   string
	AppliedAt *time.Time
	Checksum  string
	Pending   bool
}

// NewMigrator returns a Migrator backed by the embedded migrations FS.
func NewMigrator(db *sql.DB) *Migrator {
	return &Migrator{db: db, fs: migrations.FS}
}

// NewMigratorWithFS returns a Migrator using a custom FS, intended for testing.
func NewMigratorWithFS(db *sql.DB, fsys fs.ReadDirFS) *Migrator {
	return &Migrator{db: db, fs: fsys}
}

func migratorChecksum(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func detectNoTx(filename string) bool {
	return strings.HasSuffix(filename, ".no_tx.sql")
}

func (m *Migrator) ensureTrackingTable(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, createTrackingTable)
	if err != nil {
		return fmt.Errorf("ensureTrackingTable: %w", err)
	}

	return nil
}

// lockPollInterval is how often a waiting migrator retries the lock.
const lockPollInterval = 500 * time.Millisecond

// acquireLock polls pg_try_advisory_lock instead of blocking in
// pg_advisory_lock: a statement blocked on the lock keeps a snapshot open, and
// CREATE INDEX CONCURRENTLY in another replica's .no_tx migration waits for
// every such snapshot, so the two would deadlock.
func (m *Migrator) acquireLock(ctx context.Context, conn *sql.Conn) error {
	for {
		var ok bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(`+advisoryLockKey+`)`).Scan(&ok); err != nil {
			return fmt.Errorf("acquireLock: %w", err)
		}

		if ok {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("acquireLock: %w", ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}
}

func (m *Migrator) releaseLock(ctx context.Context, conn *sql.Conn) {
	// A cancelled ctx (shutdown mid-migration) must not skip the unlock: the
	// session lock would otherwise outlive this attempt on a pooled connection.
	_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+advisoryLockKey+`)`)
}

func (m *Migrator) loadApplied(ctx context.Context, conn *sql.Conn) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("loadApplied: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]string)

	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("loadApplied scan: %w", err)
		}

		applied[version] = checksum
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loadApplied iterate: %w", err)
	}

	return applied, nil
}

func (m *Migrator) applyFile(ctx context.Context, conn *sql.Conn, version string, data []byte, noTx bool) error {
	sum := migratorChecksum(data)
	if noTx {
		// One statement per query: a multi-statement simple query runs inside a
		// single implicit transaction, which CREATE INDEX CONCURRENTLY refuses.
		for i, stmt := range splitStatements(string(data)) {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("apply %s statement %d: %w", version, i+1, err)
			}
		}

		if _, err := conn.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`,
			version, sum,
		); err != nil {
			return fmt.Errorf("record %s: %w", version, err)
		}

		return nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx, string(data)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply %s: %w", version, err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`,
		version, sum,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record %s: %w", version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", version, err)
	}

	return nil
}

// Apply runs all pending migrations in lexicographic order.
// It is idempotent: already-applied migrations are skipped.
// Returns an error if any migration fails or checksum drift is detected.
func (m *Migrator) Apply(ctx context.Context) error {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: get conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := m.acquireLock(ctx, conn); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer m.releaseLock(ctx, conn)

	if err := m.ensureTrackingTable(ctx, conn); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	applied, err := m.loadApplied(ctx, conn)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	entries, err := m.fs.ReadDir(".")
	if err != nil {
		return fmt.Errorf("migrate: read embedded fs: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		version := e.Name()

		data, err := fs.ReadFile(m.fs, version)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", version, err)
		}

		current := migratorChecksum(data)

		if stored, ok := applied[version]; ok {
			if stored != current {
				return fmt.Errorf("%w: migration %s applied %s on-disk %s", ErrChecksumDrift, version, stored, current)
			}

			continue
		}

		if err := m.applyFile(ctx, conn, version, data, detectNoTx(version)); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	return nil
}

// Status returns the state of every embedded migration.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("status: get conn: %w", err)
	}
	defer func() { _ = conn.Close() }()

	rows, err := conn.QueryContext(ctx, `SELECT version, applied_at, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("status: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]MigrationStatus)

	for rows.Next() {
		var (
			ms MigrationStatus
			t  time.Time
		)
		if err := rows.Scan(&ms.Version, &t, &ms.Checksum); err != nil {
			return nil, fmt.Errorf("status: scan: %w", err)
		}

		ms.AppliedAt = &t
		applied[ms.Version] = ms
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("status: iterate: %w", err)
	}

	entries, err := m.fs.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("status: read embedded fs: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var result []MigrationStatus

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		version := e.Name()
		if ms, ok := applied[version]; ok {
			result = append(result, ms)
		} else {
			result = append(result, MigrationStatus{Version: version, Pending: true})
		}
	}

	return result, nil
}
