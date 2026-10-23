// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package responses

import "time"

// ReplicaInfoResponse describes the replica backend without any credential.
type ReplicaInfoResponse struct {
	// Type is the backend kind (filesystem, s3, gcs, azure, ...).
	Type string `json:"type"`
	// Location is the directory, bucket or container the replica writes to.
	Location string `json:"location"`
}

// ReplicationVerifyResponse is the outcome (or the progress, while the pass
// runs) of a replication verify pass.
type ReplicationVerifyResponse struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// Deep is true when the bytes of both copies were hashed.
	Deep bool `json:"deep"`
	// Listing is "list" when the primary store was enumerated and "database"
	// when it cannot list and the blobs table was walked.
	Listing string `json:"listing"`
	// ReplicaListed is false when the replica cannot enumerate its objects, so
	// blobs only it holds were not looked for.
	ReplicaListed bool `json:"replica_listed"`
	// Checked is the number of blobs examined.
	Checked int64 `json:"checked"`
	// MissingReplica blobs were missing from the replica (a copy was queued).
	MissingReplica int64 `json:"missing_replica"`
	// MissingPrimary blobs were missing from the primary store (restored from the replica).
	MissingPrimary int64 `json:"missing_primary"`
	// Mismatched blobs had different sizes (or contents, with a deep pass) on the two sides.
	Mismatched int64 `json:"mismatched"`
	// Repaired blobs were fixed or queued for copying.
	Repaired int64 `json:"repaired"`
	// Unreferenced blobs exist only on the replica and nothing needs them.
	Unreferenced int64 `json:"unreferenced"`
	// Lost blobs are referenced by the database but exist on neither side.
	Lost int64 `json:"lost"`
	// Errors is the number of blobs that could not be checked or repaired.
	Errors int64 `json:"errors"`
}

// ReplicationStatusResponse is the state of blob replication to the replica.
type ReplicationStatusResponse struct {
	// Enabled is false when no replica is configured (storage.replica.type is empty);
	// every other field is then empty.
	Enabled bool                 `json:"enabled"`
	Replica *ReplicaInfoResponse `json:"replica"`
	// ReadFallback is notfound, error or off.
	ReadFallback string `json:"read_fallback"`
	// BacklogCopy and BacklogDelete are the queued operations by kind.
	BacklogCopy   int64 `json:"backlog_copy"`
	BacklogDelete int64 `json:"backlog_delete"`
	// OldestPendingAgeSeconds is the replication lag: the age of the oldest queued operation.
	OldestPendingAgeSeconds float64 `json:"oldest_pending_age_seconds"`
	// Failing is the number of queued operations that failed repeatedly.
	Failing int64 `json:"failing"`
	// ReplicatedTotal and ReplicatedBytesTotal count what has been copied since replication began.
	ReplicatedTotal      int64 `json:"replicated_total"`
	ReplicatedBytesTotal int64 `json:"replicated_bytes_total"`
	// LastError is the most recent replication failure, empty when the last pass was clean.
	LastError   string     `json:"last_error"`
	LastErrorAt *time.Time `json:"last_error_at"`
	// VerifyRequested is true while an admin-requested verify pass waits to start.
	VerifyRequested bool                       `json:"verify_requested"`
	VerifyRunning   *ReplicationVerifyResponse `json:"verify_running"`
	LastVerify      *ReplicationVerifyResponse `json:"last_verify"`
}

// ReplicationVerifyRequestedResponse acknowledges a verify request.
type ReplicationVerifyRequestedResponse struct {
	Requested bool `json:"requested"`
}
