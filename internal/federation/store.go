// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Store is the SQL side of federation: the log of this instance, its peers,
// the inbound tokens and the deferred units. It works on the raw pool like the
// other background workers; the per-format rows are the codecs' business.
type Store struct {
	db *sql.DB
}

// NewStore returns a Store over db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// formatTables are the unit tables of each federated format. Enabling
// federation locks them against writers for the instant between registering
// the repository and backfilling its rows, so no concurrent change slips
// between the two.
var formatTables = map[string][]string{ //nolint:gochecknoglobals // constant lookup
	"raw":    {"raw_artifacts"},
	"maven":  {"maven_artifacts"},
	"npm":    {"npm_versions", "npm_dist_tags"},
	"docker": {"docker_manifests", "docker_tags"},
	"helm":   {"helm_charts"},
	"pypi":   {"pypi_packages"},
	"cargo":  {"cargo_crates"},
	"go":     {"gomod_modules"},
	"nuget":  {"nuget_packages"},
}

// RepoState is the log identity of a federated repository.
type RepoState struct {
	RepoID    string
	LogID     uuid.UUID
	HeadSeq   int64
	EnabledAt time.Time
}

// InstanceID returns the identity of this instance.
func (s *Store) InstanceID(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID

	if err := s.db.QueryRowContext(ctx, `SELECT instance_id FROM federation_instance`).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("read instance id: %w", err)
	}

	return id, nil
}

// RepoState returns the log state of a repository, ErrNotFederated when it has none.
func (s *Store) RepoState(ctx context.Context, repoID string) (*RepoState, error) {
	st := &RepoState{RepoID: repoID}

	err := s.db.QueryRowContext(ctx,
		`SELECT log_id, head_seq, enabled_at FROM federation_repos WHERE repo_id = $1::uuid`, repoID,
	).Scan(&st.LogID, &st.HeadSeq, &st.EnabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFederated
	}

	if err != nil {
		return nil, fmt.Errorf("read federation state: %w", err)
	}

	return st, nil
}

// Enable makes a repository federated: it starts its log, registers every live
// unit of the repository in it (stamped with the row's last update time) and
// sets the repositories.federated flag, all in one transaction. Enabling an
// already federated repository does nothing.
func (s *Store) Enable(ctx context.Context, repoID, format string) error {
	codec, err := CodecFor(format)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin enable federation: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	for _, table := range formatTables[format] {
		// SHARE blocks concurrent writers until we commit (and waits for the
		// ones in flight), so the backfill sees every row a writer that did
		// not log its change could have committed.
		if _, err := tx.ExecContext(ctx, `LOCK TABLE `+table+` IN SHARE MODE`); err != nil {
			return fmt.Errorf("lock %s: %w", table, err)
		}
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO federation_repos (repo_id) VALUES ($1::uuid) ON CONFLICT (repo_id) DO NOTHING`, repoID)
	if err != nil {
		return fmt.Errorf("start federation log: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}

	if err := codec.Backfill(ctx, tx, repoID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE repositories SET federated = true WHERE id = $1::uuid`, repoID); err != nil {
		return fmt.Errorf("flag repository federated: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit enable federation: %w", err)
	}

	return nil
}

// Disable stops federating a repository: its log, peers, tokens and deferred
// units are removed (the artifacts stay). Enabling it again starts a new log,
// so peers of the old one must resync.
func (s *Store) Disable(ctx context.Context, repoID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin disable federation: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM federation_repos WHERE repo_id = $1::uuid`, repoID); err != nil {
		return fmt.Errorf("remove federation log: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE repositories SET federated = false WHERE id = $1::uuid`, repoID); err != nil {
		return fmt.Errorf("unflag repository: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit disable federation: %w", err)
	}

	return nil
}

// Counts returns the number of logged units and, among them, tombstones.
func (s *Store) Counts(ctx context.Context, repoID string) (int64, int64, error) {
	var units, tombstones int64

	err := s.db.QueryRowContext(ctx,
		`SELECT count(*), count(*) FILTER (WHERE tombstone) FROM federation_units WHERE repo_id = $1::uuid`, repoID,
	).Scan(&units, &tombstones)
	if err != nil {
		return 0, 0, fmt.Errorf("count federation units: %w", err)
	}

	return units, tombstones, nil
}

const entryColumns = `seq, unit_key, kind, event, tombstone, hlc_wall, hlc_counter, origin, content_ref, changed_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanEntry(r rowScanner) (Entry, error) {
	var e Entry

	err := r.Scan(&e.Seq, &e.UnitKey, &e.Kind, &e.Event, &e.Tombstone, &e.Stamp.Wall, &e.Stamp.Counter, &e.Stamp.Origin, &e.ContentRef, &e.ChangedAt)

	return e, err
}

// ListEntries returns up to limit log entries with seq > after, in seq order.
func (s *Store) ListEntries(ctx context.Context, repoID string, after int64, limit int) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+entryColumns+` FROM federation_units WHERE repo_id = $1::uuid AND seq > $2 ORDER BY seq LIMIT $3`,
		repoID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list federation log: %w", err)
	}
	defer rows.Close()

	var out []Entry

	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan federation log: %w", err)
		}

		out = append(out, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list federation log: %w", err)
	}

	return out, nil
}

// GetEntry returns the current log row of a unit, ErrNotFound when the unit is not logged.
func (s *Store) GetEntry(ctx context.Context, q Querier, repoID, key string) (*Entry, error) {
	e, err := scanEntry(q.QueryRowContext(ctx,
		`SELECT `+entryColumns+` FROM federation_units WHERE repo_id = $1::uuid AND unit_key = $2`, repoID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("read federation unit: %w", err)
	}

	return &e, nil
}

// PurgeTombstones removes tombstones older than retention from every log and
// returns how many it removed.
func (s *Store) PurgeTombstones(ctx context.Context, retention time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM federation_units WHERE tombstone AND changed_at < now() - make_interval(secs => $1)`,
		retention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("purge federation tombstones: %w", err)
	}

	n, _ := res.RowsAffected()

	return n, nil
}

// RepoInfo is what the peer endpoints need to know about a repository.
type RepoInfo struct {
	ID     string
	Name   string
	Format string
}

// RepoByID loads the name and format of a repository (live ones only).
func (s *Store) RepoByID(ctx context.Context, repoID string) (*RepoInfo, error) {
	r := &RepoInfo{ID: repoID}

	err := s.db.QueryRowContext(ctx,
		`SELECT name, format FROM repositories WHERE id = $1::uuid AND deleted_at IS NULL`, repoID,
	).Scan(&r.Name, &r.Format)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("load repository: %w", err)
	}

	return r, nil
}

// TrashRetentionDays resolves the effective trash retention of a repository,
// falling back to def (the server default).
func (s *Store) TrashRetentionDays(ctx context.Context, q Querier, repoID string, def int) (int, error) {
	var days int

	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(trash_retention_days, $2::int) FROM repositories WHERE id = $1::uuid`, repoID, def,
	).Scan(&days)
	if err != nil {
		return 0, fmt.Errorf("resolve trash retention: %w", err)
	}

	return days, nil
}
