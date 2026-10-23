// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"time"
)

// Operations of the blob replication queue (see BlobReplicationStore).
const (
	// BlobReplicationCopy makes the blob exist on the replica.
	BlobReplicationCopy = "copy"
	// BlobReplicationDelete makes the blob disappear from the replica.
	BlobReplicationDelete = "delete"
)

// BlobReplication is a queued replication operation.
type BlobReplication struct {
	SHA256 string
	// Op is BlobReplicationCopy or BlobReplicationDelete.
	Op string
	// Version changes whenever Op changes; CompleteBlobReplication and
	// FailBlobReplication act only on the version that was claimed.
	Version    int64
	Attempts   int
	EnqueuedAt time.Time
	// NextAttemptAt, LastError are set by GetBlobReplication only.
	NextAttemptAt time.Time
	LastError     string
}

// BlobReplicationBacklog summarizes the queue.
type BlobReplicationBacklog struct {
	Copy   int64
	Delete int64
	// Failing is the number of operations with at least the attempts asked for.
	Failing int64
	// OldestAgeSeconds is the age of the oldest queued operation (0 when empty).
	OldestAgeSeconds float64
}

// BlobReplicationState is the shared singleton state of the replication
// subsystem: totals, the last error and the verify job's progress.
type BlobReplicationState struct {
	ReplicatedTotal int64
	ReplicatedBytes int64
	LastError       string
	LastErrorAt     *time.Time
	// VerifyRequested is set by an admin asking for a verify pass; the next
	// worker tick starts it.
	VerifyRequested bool
	// VerifyCursor is the last blob id the running verify pass finished.
	VerifyCursor string
	// VerifyRun is the JSON counters of the running pass (nil = none runs);
	// LastVerify those of the last finished one.
	VerifyRun  json.RawMessage
	LastVerify json.RawMessage
}

// BlobReplicationStore is the durable queue and state behind async blob
// replication (docs/operations/replication.md); *PostgresDB implements it.
type BlobReplicationStore interface {
	// EnqueueBlobReplication queues op ("copy" or "delete") for the blob. The
	// latest operation wins: a different op replaces the queued one (new
	// version, due at once), the same op again is a no-op.
	EnqueueBlobReplication(ctx context.Context, sha256, op string) error
	// ClaimBlobReplications hands out up to limit operations due at asOf. A
	// claimed row is invisible to other callers until asOf+claimFor.
	ClaimBlobReplications(ctx context.Context, asOf time.Time, limit int, claimFor time.Duration) ([]BlobReplication, error)
	// CompleteBlobReplication removes the claimed operation; false when it was
	// superseded meanwhile (a newer version stays queued).
	CompleteBlobReplication(ctx context.Context, sha256 string, version int64) (bool, error)
	// FailBlobReplication records a failed attempt: attempts+1, the error and
	// the next attempt time; false when the operation was superseded.
	FailBlobReplication(ctx context.Context, sha256 string, version int64, lastError string, nextAttempt time.Time) (bool, error)
	// ReleaseBlobReplication gives a claimed operation back unprocessed.
	ReleaseBlobReplication(ctx context.Context, sha256 string, version int64) error
	// GetBlobReplication returns the queued operation of the blob, nil when none.
	GetBlobReplication(ctx context.Context, sha256 string) (*BlobReplication, error)
	// BlobReplicationBacklog counts the queue; failingAttempts is the attempt
	// count from which an operation counts as failing.
	BlobReplicationBacklog(ctx context.Context, failingAttempts int) (BlobReplicationBacklog, error)
	// ListBlobSHAs returns up to limit blobs-table ids greater than after, ascending.
	ListBlobSHAs(ctx context.Context, after string, limit int) ([]string, error)
	// BlobReplicationReferenced reports whether the database still needs the
	// blob: a blobs row, generated repository metadata or a package row.
	BlobReplicationReferenced(ctx context.Context, sha256 string) (bool, error)

	// GetBlobReplicationState reads the singleton state.
	GetBlobReplicationState(ctx context.Context) (BlobReplicationState, error)
	// AddBlobReplicationProgress adds finished copies and their bytes.
	AddBlobReplicationProgress(ctx context.Context, copies, bytes int64) error
	// SetBlobReplicationLastError records the most recent failure.
	SetBlobReplicationLastError(ctx context.Context, message string) error
	// RequestReplicationVerify asks for a verify pass; false when one is
	// running or already requested.
	RequestReplicationVerify(ctx context.Context) (bool, error)
	// StartReplicationVerify begins a pass with the initial counters; false
	// when another replica already runs one.
	StartReplicationVerify(ctx context.Context, run json.RawMessage) (bool, error)
	// SaveReplicationVerifyProgress stores the cursor and counters of the
	// running pass; false when no pass runs.
	SaveReplicationVerifyProgress(ctx context.Context, cursor string, run json.RawMessage) (bool, error)
	// FinishReplicationVerify stores the result of the pass and ends it.
	FinishReplicationVerify(ctx context.Context, result json.RawMessage) error
}
