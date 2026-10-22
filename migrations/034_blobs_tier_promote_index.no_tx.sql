-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Promote-on-read candidates, part 2: replaces idx_blobs_tier_reads (032), whose
-- (storage_backend_id, last_accessed_at) shape could serve neither the
-- column-to-column filter nor the ORDER BY sha256 the candidate query pages by,
-- and which held every cold blob ever read, including reads from before the
-- demotion. The new partial index holds only blobs read since they reached
-- their tier, in the order the query consumes them, so a page is an index range
-- scan instead of a heap sort of everything ever read.
--
-- The predicate must stay textually identical to the WHERE clause of
-- ListPromotionCandidates (internal/database/postgres_tiering.go) for the
-- planner to prove the index covers the query.
--
-- last_accessed_at appears in the predicate, so an update of it is not a HOT
-- update (PostgreSQL treats predicate columns as indexed). The access tracker
-- bounds that to one write per blob per access resolution window, and does not
-- track reads at all while no cold tier exists; see
-- docs/operations/storage-tiering.md.
--
-- Runs outside a transaction (*.no_tx.sql: one statement at a time), after the
-- table changes of 032. Re-runnable: an interrupted CONCURRENTLY build leaves an
-- INVALID index that IF NOT EXISTS would skip for good, so it is dropped first.

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
          AND c.relname = 'idx_blobs_tier_promote'
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_blobs_tier_promote
    ON blobs (sha256)
    WHERE storage_backend_id IS NOT NULL
      AND last_accessed_at IS NOT NULL
      AND (tier_changed_at IS NULL OR last_accessed_at > tier_changed_at);

DROP INDEX CONCURRENTLY IF EXISTS idx_blobs_tier_reads;
