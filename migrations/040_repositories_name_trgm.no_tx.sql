-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- GIN trigram index for the repository half of the search (SearchRepositories
-- in internal/search/executor.go filters repositories.name with ILIKE '%term%'),
-- the one searched name column 030_perf_indexes_build.no_tx.sql did not cover.
-- pg_trgm is created by 030_perf_indexes.sql. The planner uses the index
-- whatever the other predicates are, so it is not partial.
--
-- Numbered 040 to leave 035-039 to concurrent branches; the migrator only
-- orders by name, so the gap is harmless.
--
-- Runs outside a transaction (*.no_tx.sql: one statement at a time). Re-runnable:
-- an interrupted CREATE INDEX CONCURRENTLY leaves an INVALID index that IF NOT
-- EXISTS would skip for good, so it is dropped first.

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
          AND c.relname = 'idx_repositories_name_trgm'
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_repositories_name_trgm ON repositories USING gin (name gin_trgm_ops);
