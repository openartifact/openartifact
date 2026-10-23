// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

var _ BlobReplicationStore = (*PostgresDB)(nil)

// EnqueueBlobReplication queues op for the blob (see BlobReplicationStore).
func (p *PostgresDB) EnqueueBlobReplication(ctx context.Context, sha256, op string) error {
	if err := p.q.EnqueueBlobReplication(ctx, dbsqlc.EnqueueBlobReplicationParams{Sha256: sha256, Op: op}); err != nil {
		return fmt.Errorf("enqueue blob replication: %w", err)
	}

	return nil
}

// ClaimBlobReplications hands out up to limit operations due at asOf.
func (p *PostgresDB) ClaimBlobReplications(ctx context.Context, asOf time.Time, limit int, claimFor time.Duration) ([]BlobReplication, error) {
	rows, err := p.q.ClaimBlobReplications(ctx, dbsqlc.ClaimBlobReplicationsParams{
		ClaimUntil: asOf.Add(claimFor), AsOf: asOf, Batch: toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim blob replications: %w", err)
	}

	out := make([]BlobReplication, 0, len(rows))
	for _, r := range rows {
		out = append(out, BlobReplication{SHA256: r.Sha256, Op: r.Op, Version: r.Version, Attempts: int(r.Attempts), EnqueuedAt: r.EnqueuedAt})
	}

	return out, nil
}

// CompleteBlobReplication removes the claimed operation.
func (p *PostgresDB) CompleteBlobReplication(ctx context.Context, sha256 string, version int64) (bool, error) {
	n, err := p.q.CompleteBlobReplication(ctx, dbsqlc.CompleteBlobReplicationParams{Sha256: sha256, Version: version})
	if err != nil {
		return false, fmt.Errorf("complete blob replication: %w", err)
	}

	return n > 0, nil
}

// FailBlobReplication records a failed attempt.
func (p *PostgresDB) FailBlobReplication(ctx context.Context, sha256 string, version int64, lastError string, nextAttempt time.Time) (bool, error) {
	n, err := p.q.FailBlobReplication(ctx, dbsqlc.FailBlobReplicationParams{
		LastError: lastError, NextAttemptAt: nextAttempt, Sha256: sha256, Version: version,
	})
	if err != nil {
		return false, fmt.Errorf("fail blob replication: %w", err)
	}

	return n > 0, nil
}

// ReleaseBlobReplication gives a claimed operation back unprocessed.
func (p *PostgresDB) ReleaseBlobReplication(ctx context.Context, sha256 string, version int64) error {
	if err := p.q.ReleaseBlobReplication(ctx, dbsqlc.ReleaseBlobReplicationParams{Sha256: sha256, Version: version}); err != nil {
		return fmt.Errorf("release blob replication: %w", err)
	}

	return nil
}

// GetBlobReplication returns the queued operation of the blob, nil when none.
func (p *PostgresDB) GetBlobReplication(ctx context.Context, sha256 string) (*BlobReplication, error) {
	r, err := p.q.GetBlobReplication(ctx, sha256)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // no queued operation is a normal answer
	}

	if err != nil {
		return nil, fmt.Errorf("get blob replication: %w", err)
	}

	return &BlobReplication{
		SHA256: r.Sha256, Op: r.Op, Version: r.Version, Attempts: int(r.Attempts), EnqueuedAt: r.EnqueuedAt,
		NextAttemptAt: r.NextAttemptAt, LastError: r.LastError,
	}, nil
}

// BlobReplicationBacklog counts the queue.
func (p *PostgresDB) BlobReplicationBacklog(ctx context.Context, failingAttempts int) (BlobReplicationBacklog, error) {
	r, err := p.q.BlobReplicationBacklog(ctx, toInt32(failingAttempts))
	if err != nil {
		return BlobReplicationBacklog{}, fmt.Errorf("count blob replication backlog: %w", err)
	}

	return BlobReplicationBacklog{Copy: r.CopyBacklog, Delete: r.DeleteBacklog, Failing: r.Failing, OldestAgeSeconds: r.OldestAgeSeconds}, nil
}

// ListBlobSHAs returns one keyset page of the blobs table.
func (p *PostgresDB) ListBlobSHAs(ctx context.Context, after string, limit int) ([]string, error) {
	ids, err := p.q.ListBlobSHAsAfter(ctx, dbsqlc.ListBlobSHAsAfterParams{After: after, Page: toInt32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list blob ids: %w", err)
	}

	return ids, nil
}

// BlobReplicationReferenced reports whether the database still needs the blob.
func (p *PostgresDB) BlobReplicationReferenced(ctx context.Context, sha256 string) (bool, error) {
	ok, err := p.q.BlobReplicationReferenced(ctx, sha256)
	if err != nil {
		return false, fmt.Errorf("check blob references: %w", err)
	}

	if ok {
		return true, nil
	}

	n, err := p.BlobReferenceCount(ctx, sha256)
	if err != nil {
		return false, err
	}

	return n > 0, nil
}

// GetBlobReplicationState reads the singleton state.
func (p *PostgresDB) GetBlobReplicationState(ctx context.Context) (BlobReplicationState, error) {
	r, err := p.q.GetBlobReplicationState(ctx)
	if err != nil {
		return BlobReplicationState{}, fmt.Errorf("get blob replication state: %w", err)
	}

	return BlobReplicationState{
		ReplicatedTotal: r.ReplicatedTotal, ReplicatedBytes: r.ReplicatedBytes,
		LastError: r.LastError, LastErrorAt: r.LastErrorAt,
		VerifyRequested: r.VerifyRequested, VerifyCursor: r.VerifyCursor,
		VerifyRun: r.VerifyRun, LastVerify: r.LastVerify,
	}, nil
}

// AddBlobReplicationProgress adds finished copies and their bytes.
func (p *PostgresDB) AddBlobReplicationProgress(ctx context.Context, copies, bytes int64) error {
	if err := p.q.AddBlobReplicationProgress(ctx, dbsqlc.AddBlobReplicationProgressParams{Copies: copies, Bytes: bytes}); err != nil {
		return fmt.Errorf("add blob replication progress: %w", err)
	}

	return nil
}

// SetBlobReplicationLastError records the most recent failure.
func (p *PostgresDB) SetBlobReplicationLastError(ctx context.Context, message string) error {
	if err := p.q.SetBlobReplicationLastError(ctx, message); err != nil {
		return fmt.Errorf("set blob replication last error: %w", err)
	}

	return nil
}

// RequestReplicationVerify asks for a verify pass.
func (p *PostgresDB) RequestReplicationVerify(ctx context.Context) (bool, error) {
	n, err := p.q.RequestReplicationVerify(ctx)
	if err != nil {
		return false, fmt.Errorf("request replication verify: %w", err)
	}

	return n > 0, nil
}

// StartReplicationVerify begins a pass.
func (p *PostgresDB) StartReplicationVerify(ctx context.Context, run json.RawMessage) (bool, error) {
	n, err := p.q.StartReplicationVerify(ctx, run)
	if err != nil {
		return false, fmt.Errorf("start replication verify: %w", err)
	}

	return n > 0, nil
}

// SaveReplicationVerifyProgress stores the cursor and counters of the running pass.
func (p *PostgresDB) SaveReplicationVerifyProgress(ctx context.Context, cursor string, run json.RawMessage) (bool, error) {
	n, err := p.q.SaveReplicationVerifyProgress(ctx, dbsqlc.SaveReplicationVerifyProgressParams{Cursor: cursor, Run: run})
	if err != nil {
		return false, fmt.Errorf("save replication verify progress: %w", err)
	}

	return n > 0, nil
}

// FinishReplicationVerify stores the result of the pass and ends it.
func (p *PostgresDB) FinishReplicationVerify(ctx context.Context, result json.RawMessage) error {
	if err := p.q.FinishReplicationVerify(ctx, result); err != nil {
		return fmt.Errorf("finish replication verify: %w", err)
	}

	return nil
}
