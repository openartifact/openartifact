// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package workq is the queue a web process uses to ask a worker process for
// work (server.role, docs/operations/scaling.md): a row in worker_requests per
// (kind, key). A request is made with Store.Put; a Consumer in a process that
// runs the background workers claims it, hands it to the in-process worker that
// does the job and deletes it when the job is finished.
//
// A repeated request for a key not yet finished refreshes the row instead of
// adding one, so a burst of uploads costs one regeneration, as it does inside a
// single process.
package workq

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Request is a row of worker_requests.
type Request struct {
	// Kind is what to do, e.g. "regen:npm".
	Kind string
	// Key is what to do it to: an id, or a hash of Payload.
	Key string
	// Payload is the JSON subject of kinds whose key is only a hash; nil otherwise.
	Payload []byte
	// RequestedAt is when the request was last made.
	RequestedAt time.Time
}

// ref identifies the row of a request.
type ref struct{ kind, key string }

// Store reads and writes worker_requests.
type Store struct{ db *sql.DB }

// NewStore returns a Store over db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Put records a request for (kind, key), refreshing the one already there. The
// payload is JSON or nil.
func (s *Store) Put(ctx context.Context, kind, key string, payload []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO worker_requests (kind, key, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (kind, key) DO UPDATE
		SET requested_at = clock_timestamp(), payload = EXCLUDED.payload`,
		kind, key, jsonArg(payload))
	if err != nil {
		return fmt.Errorf("workq: put %s %s: %w", kind, key, err)
	}

	return nil
}

// jsonArg is the jsonb argument for payload: NULL when there is none (an empty
// byte slice is not valid JSON).
func jsonArg(payload []byte) any {
	if len(payload) == 0 {
		return nil
	}

	return payload
}

// PutIfAbsent records a request unless one for (kind, key) is already there;
// for triggers that fire on every read and only need the work to happen soon.
func (s *Store) PutIfAbsent(ctx context.Context, kind, key string, payload []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO worker_requests (kind, key, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (kind, key) DO NOTHING`,
		kind, key, jsonArg(payload))
	if err != nil {
		return fmt.Errorf("workq: put %s %s: %w", kind, key, err)
	}

	return nil
}

// claim takes up to limit unclaimed requests of the given kinds, oldest first,
// for lease. A request whose claim expired is unclaimed: its claimer died.
func (s *Store) claim(ctx context.Context, kinds []string, limit int, lease time.Duration) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE worker_requests
		SET claimed_until = clock_timestamp() + make_interval(secs => $3)
		WHERE (kind, key) IN (
			SELECT kind, key FROM worker_requests
			WHERE kind = ANY($1)
			  AND (claimed_until IS NULL OR claimed_until < clock_timestamp())
			ORDER BY requested_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED)
		RETURNING kind, key, payload, requested_at`,
		kinds, limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("workq: claim: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var out []Request

	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.Kind, &r.Key, &r.Payload, &r.RequestedAt); err != nil {
			return nil, fmt.Errorf("workq: claim: %w", err)
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workq: claim: %w", err)
	}

	return out, nil
}

// renew extends the claim of the held requests for another lease.
func (s *Store) renew(ctx context.Context, held []ref, lease time.Duration) error {
	kinds := make([]string, len(held))
	keys := make([]string, len(held))

	for i, h := range held {
		kinds[i], keys[i] = h.kind, h.key
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE worker_requests w
		SET claimed_until = clock_timestamp() + make_interval(secs => $3)
		FROM unnest($1::text[], $2::text[]) AS h(kind, key)
		WHERE w.kind = h.kind AND w.key = h.key`,
		kinds, keys, lease.Seconds())
	if err != nil {
		return fmt.Errorf("workq: renew: %w", err)
	}

	return nil
}

// finish deletes the request for (kind, key) made at or before upTo: the work
// done covers it. A request made after upTo, while the work ran, stays and is
// released for the next claim.
func (s *Store) finish(ctx context.Context, r ref, upTo time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM worker_requests WHERE kind = $1 AND key = $2 AND requested_at <= $3`,
		r.kind, r.key, upTo); err != nil {
		return fmt.Errorf("workq: finish %s %s: %w", r.kind, r.key, err)
	}

	return s.release(ctx, r)
}

// release drops the claim on (kind, key), so the next claim, by any worker,
// can take the request at once instead of after the lease lapses.
func (s *Store) release(ctx context.Context, r ref) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE worker_requests SET claimed_until = NULL WHERE kind = $1 AND key = $2`,
		r.kind, r.key); err != nil {
		return fmt.Errorf("workq: release %s %s: %w", r.kind, r.key, err)
	}

	return nil
}
