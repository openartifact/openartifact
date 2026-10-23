-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Lookup of a repository's pending quota entries (migration 061): the exact
-- match of quota_pending_take and the per-repository sums of the reconcile.
-- quota_pending is tiny and short-lived. Re-runnable: an interrupted
-- CREATE INDEX CONCURRENTLY leaves an INVALID index that IF NOT EXISTS would
-- skip for good, so it is dropped first.

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
          AND c.relname = 'idx_quota_pending_repo'
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_quota_pending_repo
    ON quota_pending (repository_id, size_bytes, artifact_count, id);
