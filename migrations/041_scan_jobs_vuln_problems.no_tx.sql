-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- The cross-repository vulnerability overview (GET /api/v1/vuln/scans) filters
-- by scan status. Failed and partial scans are the rare, interesting ones, so
-- they get a small partial index: the planner starts from it and reaches the
-- result row through the unique vuln_results(scan_job_id). The other filters
-- are already served: critical findings (idx_vuln_results_critical),
-- quarantined (idx_vuln_results_quarantined) and the repository
-- (idx_artifact_refs_repository_id).
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
          AND c.relname = 'idx_scan_jobs_vuln_problems'
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_scan_jobs_vuln_problems ON scan_jobs (status, created_at DESC)
    WHERE scan_type = 'vuln' AND status IN ('failed', 'partial');
