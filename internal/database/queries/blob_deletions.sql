-- Deferred blob deletion and backup leases (migration 035).

-- name: EnqueueBlobDeletion :exec
-- Queues the copies named by scope for deletion once not_before has passed and
-- no live backup snapshot can still need them. Deleting the same blob again
-- refreshes the row, so deleted_xid always belongs to the latest deletion.
INSERT INTO blob_deletions (sha256, scope, not_before)
VALUES (@sha256::text, @scope::text, now() + make_interval(secs => @grace_seconds::float8))
ON CONFLICT (sha256, scope) DO UPDATE
SET queued_at = now(),
    deleted_xid = (pg_current_xact_id()::text)::bigint,
    not_before = EXCLUDED.not_before,
    claimed_until = NULL,
    attempts = 0;

-- name: ClaimDueBlobDeletions :many
-- Hands out up to @batch deletions that are due: past their grace period and
-- not needed by any live backup lease. SKIP LOCKED plus claimed_until keep two
-- sweepers (any replica) from taking the same row. A lease whose snapshot is
-- not recorded yet blocks everything; a lease blocks a deletion exactly when
-- the deleting transaction is not visible in the lease's snapshot.
UPDATE blob_deletions d
SET claimed_until = now() + make_interval(secs => @claim_seconds::float8),
    attempts = d.attempts + 1
FROM (
    SELECT c.sha256, c.scope
    FROM blob_deletions c
    WHERE c.not_before <= now()
      AND (c.claimed_until IS NULL OR c.claimed_until <= now())
      AND NOT EXISTS (
          SELECT 1 FROM backup_leases l
          WHERE l.expires_at > now()
            AND (l.snapshot IS NULL
                 OR NOT pg_visible_in_snapshot((c.deleted_xid::text)::xid8, l.snapshot::pg_snapshot))
      )
    ORDER BY c.not_before, c.sha256
    LIMIT @batch::int
    FOR UPDATE OF c SKIP LOCKED
) picked
WHERE d.sha256 = picked.sha256 AND d.scope = picked.scope
RETURNING d.sha256, d.scope, d.queued_at, d.not_before, d.attempts;

-- name: BlobDeletionDue :one
-- Whether the deletion is still due right now (the sweeper asks again under the
-- per-blob lock): the row exists, its grace period has passed and no live
-- backup lease still needs the bytes. Ignores claimed_until.
SELECT EXISTS (
    SELECT 1 FROM blob_deletions c
    WHERE c.sha256 = @sha256::text AND c.scope = @scope::text
      AND c.not_before <= now()
      AND NOT EXISTS (
          SELECT 1 FROM backup_leases l
          WHERE l.expires_at > now()
            AND (l.snapshot IS NULL
                 OR NOT pg_visible_in_snapshot((c.deleted_xid::text)::xid8, l.snapshot::pg_snapshot))
      )
)::boolean AS due;

-- name: DeleteBlobDeletion :exec
DELETE FROM blob_deletions WHERE sha256 = @sha256::text AND scope = @scope::text;

-- name: CountBlobDeletions :one
SELECT count(*) FROM blob_deletions;

-- name: CreateBackupLease :one
INSERT INTO backup_leases (expires_at)
VALUES (now() + make_interval(secs => @ttl_seconds::float8))
RETURNING id;

-- name: SetBackupLeaseSnapshot :exec
UPDATE backup_leases SET snapshot = @snapshot::text WHERE id = @id;

-- name: RenewBackupLease :execrows
UPDATE backup_leases
SET heartbeat_at = now(), expires_at = now() + make_interval(secs => @ttl_seconds::float8)
WHERE id = @id;

-- name: DeleteBackupLease :exec
DELETE FROM backup_leases WHERE id = @id;

-- name: DeleteExpiredBackupLeases :execrows
DELETE FROM backup_leases WHERE expires_at <= now();

-- name: CountLiveBackupLeases :one
SELECT count(*) FROM backup_leases WHERE expires_at > now();

-- name: ListBlobsAfter :many
-- One keyset page of the stored objects, ordered by sha256 (a full backup reads
-- it through its snapshot): the blobs table plus the generated metadata and
-- deltas that live in the same content-addressed key space without a blobs row.
SELECT o.sha256::text AS sha256, max(o.size_bytes)::bigint AS size_bytes
FROM (
    SELECT sha256, size_bytes FROM blobs
    UNION ALL
    SELECT blob_sha256, blob_size FROM repository_metadata
    UNION ALL
    SELECT delta_sha256, delta_size FROM package_deltas
) o
WHERE o.sha256 > @after::text
GROUP BY o.sha256
ORDER BY o.sha256
LIMIT @page_size::int;

-- name: CountBlobs :one
-- The number of stored objects ListBlobsAfter pages through.
SELECT count(*) FROM (
    SELECT sha256 FROM blobs
    UNION
    SELECT blob_sha256 FROM repository_metadata
    UNION
    SELECT delta_sha256 FROM package_deltas
) o;
