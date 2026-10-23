-- Asynchronous blob replication queue and state (migration 067).

-- name: EnqueueBlobReplication :exec
-- Queues op for the blob. The latest operation wins: a different op replaces
-- the queued one (new version, fresh attempt counter, due at once); the same
-- op again leaves the row, and its backoff, alone.
INSERT INTO blob_replication (sha256, op)
VALUES (@sha256::text, @op::text)
ON CONFLICT (sha256) DO UPDATE
SET op = EXCLUDED.op,
    version = blob_replication.version + 1,
    attempts = 0,
    next_attempt_at = now(),
    claimed_until = NULL,
    last_error = '',
    enqueued_at = now()
WHERE blob_replication.op <> EXCLUDED.op;

-- name: ClaimBlobReplications :many
-- Hands out up to @batch operations that are due at @as_of and not claimed.
-- SKIP LOCKED plus claimed_until (set to @claim_until) keep two workers (any
-- replica) from taking the same row; a crashed worker's rows come back when
-- its claim expires. The caller supplies the instants so that backoff can be
-- tested with an injected clock.
UPDATE blob_replication r
SET claimed_until = @claim_until::timestamptz
FROM (
    SELECT c.sha256
    FROM blob_replication c
    WHERE c.next_attempt_at <= @as_of::timestamptz
      AND (c.claimed_until IS NULL OR c.claimed_until <= @as_of::timestamptz)
    ORDER BY c.next_attempt_at, c.sha256
    LIMIT @batch::int
    FOR UPDATE OF c SKIP LOCKED
) picked
WHERE r.sha256 = picked.sha256
RETURNING r.sha256, r.op, r.version, r.attempts, r.enqueued_at;

-- name: CompleteBlobReplication :execrows
DELETE FROM blob_replication WHERE sha256 = @sha256::text AND version = @version::bigint;

-- name: FailBlobReplication :execrows
UPDATE blob_replication
SET attempts = attempts + 1,
    last_error = @last_error::text,
    next_attempt_at = @next_attempt_at::timestamptz,
    claimed_until = NULL
WHERE sha256 = @sha256::text AND version = @version::bigint;

-- name: ReleaseBlobReplication :exec
-- Gives a claimed row back untouched (the worker stopped before it ran).
UPDATE blob_replication SET claimed_until = NULL WHERE sha256 = @sha256::text AND version = @version::bigint;

-- name: GetBlobReplication :one
SELECT sha256, op, version, attempts, next_attempt_at, last_error, enqueued_at
FROM blob_replication WHERE sha256 = @sha256::text;

-- name: BlobReplicationBacklog :one
SELECT
    count(*) FILTER (WHERE op = 'copy')::bigint AS copy_backlog,
    count(*) FILTER (WHERE op = 'delete')::bigint AS delete_backlog,
    count(*) FILTER (WHERE attempts >= @failing_attempts::int)::bigint AS failing,
    COALESCE(EXTRACT(EPOCH FROM (now() - min(enqueued_at))), 0)::float8 AS oldest_age_seconds
FROM blob_replication;

-- name: ListBlobSHAsAfter :many
-- One keyset page of the blobs table (the verify job's listing when the
-- primary backend cannot enumerate its objects).
SELECT sha256 FROM blobs WHERE sha256 > @after::text ORDER BY sha256 LIMIT @page::int;

-- name: BlobReplicationReferenced :one
-- Whether something in the database still needs the blob: a blobs row,
-- generated repository metadata or a current disaster-recovery sidecar (which
-- has no blobs row, see migration 068). Package-table references are counted
-- by BlobReferenceCount.
SELECT (
    EXISTS (SELECT 1 FROM blobs WHERE sha256 = @sha256::text)
    OR EXISTS (SELECT 1 FROM repository_metadata WHERE blob_sha256 = @sha256::text)
    OR EXISTS (SELECT 1 FROM recovery_sidecars WHERE sidecar_sha256 = @sha256::text)
    OR EXISTS (SELECT 1 FROM recovery_sidecars WHERE pending_sha256 = @sha256::text)
)::boolean AS referenced;

-- name: GetBlobReplicationState :one
SELECT replicated_total, replicated_bytes, last_error, last_error_at,
       verify_requested, verify_cursor, verify_run, last_verify
FROM blob_replication_state WHERE id = 1;

-- name: AddBlobReplicationProgress :exec
UPDATE blob_replication_state
SET replicated_total = replicated_total + @copies::bigint,
    replicated_bytes = replicated_bytes + @bytes::bigint
WHERE id = 1;

-- name: SetBlobReplicationLastError :exec
UPDATE blob_replication_state SET last_error = @message::text, last_error_at = now() WHERE id = 1;

-- name: RequestReplicationVerify :execrows
-- Asks for a verify pass; no row is touched (0) while one is running or
-- already requested.
UPDATE blob_replication_state SET verify_requested = true
WHERE id = 1 AND NOT verify_requested AND verify_run IS NULL;

-- name: StartReplicationVerify :execrows
-- Begins a pass: only one of several replicas that notice the request wins.
UPDATE blob_replication_state
SET verify_requested = false, verify_cursor = '', verify_run = @run::jsonb
WHERE id = 1 AND verify_run IS NULL;

-- name: SaveReplicationVerifyProgress :execrows
UPDATE blob_replication_state
SET verify_cursor = @cursor::text, verify_run = @run::jsonb
WHERE id = 1 AND verify_run IS NOT NULL;

-- name: FinishReplicationVerify :exec
UPDATE blob_replication_state
SET last_verify = @result::jsonb, verify_run = NULL, verify_cursor = '', verify_requested = false
WHERE id = 1;
