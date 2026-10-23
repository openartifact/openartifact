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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openartifact/openartifact/internal/crypto"
)

// Peer is a pull source of a federated repository with its replication state.
// The secret fields hold the stored (encrypted) values; use PeerSecrets to
// open them.
type Peer struct {
	ID      string
	RepoID  string
	Name    string
	BaseURL string
	Enabled bool

	TokenEnc      string
	CAPEM         string
	ClientCertEnc string
	ClientKeyEnc  string

	CursorSeq       int64
	RemoteHeadSeq   int64
	RemoteLogID     *uuid.UUID
	LastAttemptAt   *time.Time
	LastSuccessAt   *time.Time
	LastError       string
	ErrorCount      int
	NextAttemptAt   *time.Time
	OldestPendingAt *time.Time
	AppliedTotal    int64
	ConflictsTotal  int64
	ResyncRequested bool
	CreatedAt       time.Time

	DeferredUnits int
}

// PeerSecrets are a peer's credentials in clear.
type PeerSecrets struct {
	Token      string
	ClientCert string
	ClientKey  string
}

// Secrets decrypts the peer's credentials.
func (p *Peer) Secrets() (PeerSecrets, error) {
	var (
		s   PeerSecrets
		err error
	)

	if s.Token, err = crypto.Decrypt(p.TokenEnc); err != nil {
		return PeerSecrets{}, fmt.Errorf("decrypt peer token: %w", err)
	}

	if s.ClientCert, err = crypto.Decrypt(p.ClientCertEnc); err != nil {
		return PeerSecrets{}, fmt.Errorf("decrypt peer client certificate: %w", err)
	}

	if s.ClientKey, err = crypto.Decrypt(p.ClientKeyEnc); err != nil {
		return PeerSecrets{}, fmt.Errorf("decrypt peer client key: %w", err)
	}

	return s, nil
}

// PeerState summarises where a peer stands, for the API and the UI.
type PeerState string

// Peer states.
const (
	PeerOK       PeerState = "ok"
	PeerLagging  PeerState = "lagging"
	PeerError    PeerState = "error"
	PeerPending  PeerState = "pending"
	PeerDisabled PeerState = "disabled"
)

// Backlog is the number of log positions the peer has not been pulled yet.
func (p *Peer) Backlog() int64 {
	return max(p.RemoteHeadSeq-p.CursorSeq, 0)
}

// Lag is how long the oldest unpulled change of the peer has been waiting.
func (p *Peer) Lag(now time.Time) time.Duration {
	if p.Backlog() == 0 || p.OldestPendingAt == nil {
		return 0
	}

	return max(now.Sub(*p.OldestPendingAt), 0)
}

// State classifies the peer.
func (p *Peer) State() PeerState {
	switch {
	case !p.Enabled:
		return PeerDisabled
	case p.ErrorCount > 0:
		return PeerError
	case p.LastSuccessAt == nil:
		return PeerPending
	case p.Backlog() > 0 || p.ResyncRequested:
		return PeerLagging
	}

	return PeerOK
}

// NewPeer is the input of CreatePeer. Secrets are given in clear and stored encrypted.
type NewPeer struct {
	RepoID     string
	Name       string
	BaseURL    string
	Token      string
	CAPEM      string
	ClientCert string
	ClientKey  string
}

const peerColumns = `p.id::text, p.repo_id::text, p.name, p.base_url, p.enabled, p.token_enc, p.ca_pem, p.client_cert_enc, p.client_key_enc,
	p.cursor_seq, p.remote_head_seq, p.remote_log_id, p.last_attempt_at, p.last_success_at, p.last_error, p.error_count,
	p.next_attempt_at, p.oldest_pending_at, p.applied_total, p.conflicts_total, p.resync_requested, p.created_at,
	(SELECT count(*) FROM federation_deferred d WHERE d.peer_id = p.id)`

func scanPeer(r rowScanner) (*Peer, error) {
	var p Peer

	err := r.Scan(&p.ID, &p.RepoID, &p.Name, &p.BaseURL, &p.Enabled, &p.TokenEnc, &p.CAPEM, &p.ClientCertEnc, &p.ClientKeyEnc,
		&p.CursorSeq, &p.RemoteHeadSeq, &p.RemoteLogID, &p.LastAttemptAt, &p.LastSuccessAt, &p.LastError, &p.ErrorCount,
		&p.NextAttemptAt, &p.OldestPendingAt, &p.AppliedTotal, &p.ConflictsTotal, &p.ResyncRequested, &p.CreatedAt, &p.DeferredUnits)
	if err != nil {
		return nil, err //nolint:wrapcheck // wrapped by callers
	}

	return &p, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreatePeer adds a pull source to a federated repository.
func (s *Store) CreatePeer(ctx context.Context, n NewPeer) (*Peer, error) {
	tokenEnc, err := crypto.Encrypt(n.Token)
	if err != nil {
		return nil, fmt.Errorf("encrypt peer token: %w", err)
	}

	certEnc, err := crypto.Encrypt(n.ClientCert)
	if err != nil {
		return nil, fmt.Errorf("encrypt client certificate: %w", err)
	}

	keyEnc, err := crypto.Encrypt(n.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt client key: %w", err)
	}

	var id string

	err = s.db.QueryRowContext(ctx,
		`INSERT INTO federation_peers (repo_id, name, base_url, token_enc, ca_pem, client_cert_enc, client_key_enc)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
		n.RepoID, n.Name, n.BaseURL, tokenEnc, n.CAPEM, certEnc, keyEnc).Scan(&id)

	switch {
	case isUniqueViolation(err):
		return nil, fmt.Errorf("%w: a peer named %q already exists", ErrConflict, n.Name)
	case err != nil:
		return nil, fmt.Errorf("create peer: %w", err)
	}

	return s.GetPeer(ctx, n.RepoID, id)
}

// PeerPatch is a partial update of a peer; nil fields are left alone.
type PeerPatch struct {
	Name       *string
	BaseURL    *string
	Token      *string
	CAPEM      *string
	ClientCert *string
	ClientKey  *string
	Enabled    *bool
}

// UpdatePeer applies a partial update. Changing the URL or a credential clears
// the peer's error state so the next pull starts at once.
func (s *Store) UpdatePeer(ctx context.Context, repoID, id string, patch PeerPatch) (*Peer, error) {
	cur, err := s.GetPeer(ctx, repoID, id)
	if err != nil {
		return nil, err
	}

	if patch.Name != nil {
		cur.Name = *patch.Name
	}

	secrets, err := cur.Secrets()
	if err != nil {
		// A secret that cannot be opened (the key changed) may be replaced.
		secrets = PeerSecrets{}
	}

	apply(&cur.BaseURL, patch.BaseURL)
	apply(&cur.CAPEM, patch.CAPEM)
	apply(&secrets.Token, patch.Token)
	apply(&secrets.ClientCert, patch.ClientCert)
	apply(&secrets.ClientKey, patch.ClientKey)

	if patch.Enabled != nil {
		cur.Enabled = *patch.Enabled
	}

	tokenEnc, err := crypto.Encrypt(secrets.Token)
	if err != nil {
		return nil, fmt.Errorf("encrypt peer token: %w", err)
	}

	certEnc, err := crypto.Encrypt(secrets.ClientCert)
	if err != nil {
		return nil, fmt.Errorf("encrypt client certificate: %w", err)
	}

	keyEnc, err := crypto.Encrypt(secrets.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt client key: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE federation_peers SET name = $3, base_url = $4, token_enc = $5, ca_pem = $6, client_cert_enc = $7,
		        client_key_enc = $8, enabled = $9,
		        error_count = CASE WHEN $10 THEN 0 ELSE error_count END,
		        last_error = CASE WHEN $10 THEN '' ELSE last_error END,
		        next_attempt_at = CASE WHEN $10 THEN NULL ELSE next_attempt_at END
		  WHERE id = $2::uuid AND repo_id = $1::uuid`,
		repoID, id, cur.Name, cur.BaseURL, tokenEnc, cur.CAPEM, certEnc, keyEnc, cur.Enabled,
		patch.BaseURL != nil || patch.Token != nil || patch.CAPEM != nil || patch.ClientCert != nil || patch.ClientKey != nil)

	switch {
	case isUniqueViolation(err):
		return nil, fmt.Errorf("%w: a peer named %q already exists", ErrConflict, cur.Name)
	case err != nil:
		return nil, fmt.Errorf("update peer: %w", err)
	}

	return s.GetPeer(ctx, repoID, id)
}

func apply(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

// GetPeer loads one peer of a repository.
func (s *Store) GetPeer(ctx context.Context, repoID, id string) (*Peer, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrNotFound
	}

	p, err := scanPeer(s.db.QueryRowContext(ctx,
		`SELECT `+peerColumns+` FROM federation_peers p WHERE p.id = $1::uuid AND p.repo_id = $2::uuid`, id, repoID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("load peer: %w", err)
	}

	return p, nil
}

// ListPeers returns the peers of a repository by name; repoID "" lists the
// enabled peers of every repository (the pull scheduler).
func (s *Store) ListPeers(ctx context.Context, repoID string) ([]*Peer, error) {
	query := `SELECT ` + peerColumns + ` FROM federation_peers p `

	args := []any{}

	if repoID != "" {
		query += `WHERE p.repo_id = $1::uuid `

		args = append(args, repoID)
	} else {
		query += `WHERE p.enabled `
	}

	rows, err := s.db.QueryContext(ctx, query+`ORDER BY p.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	defer rows.Close()

	var out []*Peer

	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}

		out = append(out, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}

	return out, nil
}

// DeletePeer removes a peer and its deferred units.
func (s *Store) DeletePeer(ctx context.Context, repoID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM federation_peers WHERE id = $1::uuid AND repo_id = $2::uuid`, id, repoID)
	if err != nil {
		return fmt.Errorf("delete peer: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	return nil
}

// RequestResync flags a peer for a full resync: its cursor is reset to the
// start of the remote log and every unit is merged again (idempotently) on the
// next pull. Existing deferred units are retried at once.
func (s *Store) RequestResync(ctx context.Context, repoID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotFound
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE federation_peers SET resync_requested = true, error_count = 0, last_error = '', next_attempt_at = NULL
		  WHERE id = $1::uuid AND repo_id = $2::uuid`, id, repoID)
	if err != nil {
		return fmt.Errorf("request resync: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}

	_, err = s.db.ExecContext(ctx, `UPDATE federation_deferred SET next_attempt_at = now() WHERE peer_id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("release deferred units: %w", err)
	}

	return nil
}

// Deferred is a unit set aside after failing to apply.
type Deferred struct {
	UnitKey       string
	Seq           int64
	Attempts      int
	Reason        string
	LastError     string
	FirstSeenAt   time.Time
	NextAttemptAt time.Time
}

// ListDeferred lists a peer's deferred units, oldest first.
func (s *Store) ListDeferred(ctx context.Context, peerID string, dueOnly bool, limit int) ([]Deferred, error) {
	query := `SELECT unit_key, seq, attempts, reason, last_error, first_seen_at, next_attempt_at
	            FROM federation_deferred WHERE peer_id = $1::uuid`
	if dueOnly {
		query += ` AND next_attempt_at <= now()`
	}

	rows, err := s.db.QueryContext(ctx, query+` ORDER BY first_seen_at LIMIT $2`, peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deferred units: %w", err)
	}
	defer rows.Close()

	var out []Deferred

	for rows.Next() {
		var d Deferred
		if err := rows.Scan(&d.UnitKey, &d.Seq, &d.Attempts, &d.Reason, &d.LastError, &d.FirstSeenAt, &d.NextAttemptAt); err != nil {
			return nil, fmt.Errorf("scan deferred unit: %w", err)
		}

		out = append(out, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deferred units: %w", err)
	}

	return out, nil
}

// maxDeferBackoff caps the retry delay of a deferred unit.
const maxDeferBackoff = time.Hour

// Defer parks a unit that could not be applied; it is retried later on its own
// backoff (30s doubling to an hour) without holding up the stream.
func (s *Store) Defer(ctx context.Context, peerID, key string, seq int64, reason, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO federation_deferred (peer_id, unit_key, seq, attempts, reason, last_error, next_attempt_at)
		 VALUES ($1::uuid, $2, $3, 1, $4, $5, now() + interval '30 seconds')
		 ON CONFLICT (peer_id, unit_key) DO UPDATE
		    SET seq = EXCLUDED.seq, attempts = federation_deferred.attempts + 1, reason = EXCLUDED.reason,
		        last_error = EXCLUDED.last_error,
		        next_attempt_at = now() + make_interval(secs => LEAST($6::float8, 30 * power(2, LEAST(federation_deferred.attempts, 12))))`,
		peerID, key, seq, reason, truncate(errMsg, 500), maxDeferBackoff.Seconds())
	if err != nil {
		return fmt.Errorf("defer unit: %w", err)
	}

	return nil
}

// ClearDeferred forgets a deferred unit (applied, or superseded).
func (s *Store) ClearDeferred(ctx context.Context, peerID, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM federation_deferred WHERE peer_id = $1::uuid AND unit_key = $2`, peerID, key)
	if err != nil {
		return fmt.Errorf("clear deferred unit: %w", err)
	}

	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}
