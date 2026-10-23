// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Progress is what a pull persists about a peer.
type Progress struct {
	Cursor        int64
	RemoteHead    int64
	RemoteLogID   uuid.UUID
	OldestPending *time.Time
	// Applied and Conflicts are added to the peer's totals.
	Applied   int64
	Conflicts int64
	// ClearResync drops the resync request, once the full pass completed.
	ClearResync bool
}

// MarkAttempt records that a pull of the peer started.
func (s *Store) MarkAttempt(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE federation_peers SET last_attempt_at = now() WHERE id = $1::uuid`, id); err != nil {
		return fmt.Errorf("mark federation attempt: %w", err)
	}

	return nil
}

// SaveProgress persists the cursor and counters after a page was applied.
func (s *Store) SaveProgress(ctx context.Context, id string, p Progress) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE federation_peers
		    SET cursor_seq = $2, remote_head_seq = $3, remote_log_id = $4::uuid, oldest_pending_at = $5,
		        applied_total = applied_total + $6, conflicts_total = conflicts_total + $7,
		        resync_requested = resync_requested AND NOT $8
		  WHERE id = $1::uuid`,
		id, p.Cursor, p.RemoteHead, p.RemoteLogID.String(), p.OldestPending, p.Applied, p.Conflicts, p.ClearResync)
	if err != nil {
		return fmt.Errorf("save federation progress: %w", err)
	}

	return nil
}

// MarkSuccess records a pull that completed without error.
func (s *Store) MarkSuccess(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE federation_peers
		    SET last_success_at = now(), error_count = 0, last_error = '', next_attempt_at = NULL
		  WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("mark federation success: %w", err)
	}

	return nil
}

// MarkFailure records a failed pull and when the next one may start.
func (s *Store) MarkFailure(ctx context.Context, id, msg string, retryIn time.Duration) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE federation_peers
		    SET error_count = error_count + 1, last_error = $2,
		        next_attempt_at = now() + make_interval(secs => $3::float8)
		  WHERE id = $1::uuid`, id, truncate(msg, 500), retryIn.Seconds())
	if err != nil {
		return fmt.Errorf("mark federation failure: %w", err)
	}

	return nil
}

// DuePeers lists the enabled peers whose next pull is due: not backing off
// and, unless a resync is requested, last tried at least poll ago.
func (s *Store) DuePeers(ctx context.Context, poll time.Duration) ([]*Peer, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+peerColumns+` FROM federation_peers p
		  WHERE p.enabled
		    AND (p.next_attempt_at IS NULL OR p.next_attempt_at <= now())
		    AND (p.resync_requested OR p.last_attempt_at IS NULL
		         OR p.last_attempt_at <= now() - make_interval(secs => $1::float8))
		  ORDER BY p.last_attempt_at NULLS FIRST`, poll.Seconds())
	if err != nil {
		return nil, fmt.Errorf("list due federation peers: %w", err)
	}
	defer rows.Close()

	var out []*Peer

	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due federation peers: %w", err)
	}

	return out, nil
}
