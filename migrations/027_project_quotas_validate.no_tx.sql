-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Follow-up of 027_project_quotas.sql, run one statement at a time without
-- blocking writes. Re-runnable: an interrupted CONCURRENTLY build leaves an
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
          AND c.relname = 'idx_cleanup_policies_project'
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

ALTER TABLE cleanup_policies VALIDATE CONSTRAINT chk_cleanup_policy_scope;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_cleanup_policies_project ON cleanup_policies (project_id) WHERE project_id IS NOT NULL;
