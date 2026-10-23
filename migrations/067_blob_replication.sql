-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Asynchronous blob replication to a secondary storage backend
-- (docs/operations/replication.md).
--
-- When storage.replica is configured, every blob that lands on the primary
-- store and every blob that is deleted from it leaves one row here, so the
-- replica follows the primary without blocking an upload on it. The tables are
-- created empty and stay empty (and unused) while no replica is configured.
--
-- Only new tables here; their indexes are built with them (they start empty).

SET LOCAL lock_timeout = '10s';

-- The replication queue: at most one row per blob, the latest operation wins
-- (a copy supersedes a pending delete and the other way round).
CREATE TABLE blob_replication (
    sha256 character varying(64) NOT NULL,
    -- 'copy': the blob must exist on the replica; 'delete': it must not.
    op text NOT NULL,
    -- Bumped whenever the operation changes. A worker finishes or fails a row
    -- only while its version is still the one it claimed, so an operation that
    -- was superseded while in flight is never lost.
    version bigint DEFAULT 1 NOT NULL,
    -- Failed attempts since the operation was queued; drives the backoff.
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    -- A worker that picked the row up holds it until this time, so another
    -- replica does not process it too; a crashed worker's rows come back.
    claimed_until timestamp with time zone,
    last_error text DEFAULT '' NOT NULL,
    enqueued_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT blob_replication_pkey PRIMARY KEY (sha256),
    CONSTRAINT blob_replication_sha256_hex CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT blob_replication_op_check CHECK (op IN ('copy', 'delete'))
);

CREATE INDEX idx_blob_replication_due ON blob_replication (next_attempt_at);

COMMENT ON TABLE blob_replication IS 'Pending blob replication work: one row per blob, op = copy or delete; the latest operation wins.';
COMMENT ON COLUMN blob_replication.version IS 'Bumped when op changes; a worker completes or fails a row only for the version it claimed.';

-- Singleton: replication totals, the last error and the verify job's progress,
-- shared by every replica so the status endpoint answers the same everywhere.
CREATE TABLE blob_replication_state (
    id smallint DEFAULT 1 NOT NULL,
    replicated_total bigint DEFAULT 0 NOT NULL,
    replicated_bytes bigint DEFAULT 0 NOT NULL,
    last_error text DEFAULT '' NOT NULL,
    last_error_at timestamp with time zone,
    -- An admin asked for a verify pass (POST /api/v1/admin/storage/replication/verify).
    verify_requested boolean DEFAULT false NOT NULL,
    -- Last blob id the running verify pass finished ('' = from the start).
    verify_cursor text DEFAULT '' NOT NULL,
    -- Counters of the running pass (NULL = none is running) and of the last finished one.
    verify_run jsonb,
    last_verify jsonb,
    CONSTRAINT blob_replication_state_pkey PRIMARY KEY (id),
    CONSTRAINT blob_replication_state_singleton CHECK (id = 1)
);

INSERT INTO blob_replication_state (id) VALUES (1);

COMMENT ON TABLE blob_replication_state IS 'Singleton row: blob replication totals and verify progress.';
