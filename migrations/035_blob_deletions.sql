-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Deferred physical deletion of blob bytes and backup leases
-- (docs/operations/backup.md, docs/operations/storage.md).
--
-- A full backup reads its records from one database snapshot and the blobs
-- those records name afterwards. A blob that is deleted (or a tier copy that is
-- retired) while the backup runs would then be missing from the archive, so
-- deleting the bytes is decoupled from deleting the row: the transaction that
-- removes a blobs row (or flips a placement) enqueues the bytes in
-- blob_deletions, and the sweeper deletes them once no live backup could still
-- need them.
--
-- Only new tables here; their indexes are built with them (they start empty).

SET LOCAL lock_timeout = '10s';

-- One row per blob copy whose bytes are waiting to be deleted.
--
-- scope names the copies to delete: '*' = every copy of the blob on every tier
-- (the blob itself was deleted), '' = the copy on the primary store, otherwise
-- the storage_backends id of the tier whose copy a placement flip retired.
-- Deleting the same blob again refreshes the row, so deleted_xid always belongs
-- to the most recent deletion.
CREATE TABLE blob_deletions (
    sha256 character varying(64) NOT NULL,
    scope text NOT NULL,
    queued_at timestamp with time zone DEFAULT now() NOT NULL,
    -- Top-level transaction id (xid8, kept as bigint) of the deleting
    -- transaction. A backup snapshot sees the deletion iff this id is visible
    -- in it (pg_visible_in_snapshot), which is exactly the condition for the
    -- backup not needing the bytes any more.
    deleted_xid bigint DEFAULT (pg_current_xact_id()::text)::bigint NOT NULL,
    -- Grace period: the bytes are never deleted before this time.
    not_before timestamp with time zone NOT NULL,
    -- A sweeper that picked the row up holds it until this time, so another
    -- replica does not process it too; a crashed sweeper's rows come back.
    claimed_until timestamp with time zone,
    attempts integer DEFAULT 0 NOT NULL,
    CONSTRAINT blob_deletions_pkey PRIMARY KEY (sha256, scope)
);

CREATE INDEX idx_blob_deletions_not_before ON blob_deletions (not_before);

COMMENT ON TABLE blob_deletions IS 'Blob copies whose bytes are waiting to be deleted: after the grace period and once no live backup snapshot can still need them (backup_leases).';
COMMENT ON COLUMN blob_deletions.scope IS '''*'' = every copy of the blob; '''' = the primary store copy; otherwise the storage_backends id of the tier whose copy is retired.';
COMMENT ON COLUMN blob_deletions.deleted_xid IS 'Top-level transaction id of the deleting transaction; compared with backup_leases.snapshot.';

-- One row per running backup. The backup registers it before it takes its
-- database snapshot, renews it while it runs and removes it when it ends; a
-- crashed backup's lease simply expires.
CREATE TABLE backup_leases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    -- pg_current_snapshot() of the backup's snapshot; NULL until it is taken,
    -- which blocks every pending deletion.
    snapshot text,
    snapshot_at timestamp with time zone DEFAULT now() NOT NULL,
    heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT backup_leases_pkey PRIMARY KEY (id)
);

CREATE INDEX idx_backup_leases_expires_at ON backup_leases (expires_at);

COMMENT ON TABLE backup_leases IS 'Running backups; while a lease is live the blob bytes its snapshot may still reference are not deleted (blob_deletions).';
