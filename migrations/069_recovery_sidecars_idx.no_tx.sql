-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Lookup of a journal sidecar by blob id (migration 069): the replication
-- verify pass asks, per blob, whether a recovery sidecar still needs it, so
-- that a primary store refilled from the replica gets its sidecars back.
-- Re-runnable: an interrupted CREATE INDEX CONCURRENTLY leaves an INVALID
-- index that IF NOT EXISTS would skip for good, so it is dropped first.

DO $$
DECLARE
    idx regclass;
BEGIN
    SET LOCAL lock_timeout = '10s';

    FOR idx IN
        SELECT i.indexrelid::regclass
        FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        WHERE NOT i.indisvalid
          AND c.relnamespace = to_regnamespace(current_schema())
          AND c.relname IN ('idx_recovery_sidecars_sha', 'idx_recovery_sidecars_pending')
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_recovery_sidecars_sha
    ON recovery_sidecars (sidecar_sha256);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_recovery_sidecars_pending
    ON recovery_sidecars (pending_sha256) WHERE pending_sha256 IS NOT NULL;
