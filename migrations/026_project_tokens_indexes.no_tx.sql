-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Indexes of 026_project_tokens.sql, built without blocking writes (*.no_tx.sql:
-- one statement at a time). Re-runnable: an interrupted CONCURRENTLY build
-- leaves an INVALID index that IF NOT EXISTS would skip for good, so those are
-- dropped first.
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
          AND c.relname IN ('idx_auth_tokens_project_name', 'idx_auth_tokens_created_by', 'idx_auth_users_project')
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_tokens_project_name ON auth_tokens (project_id, name) WHERE project_id IS NOT NULL AND revoked_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_tokens_created_by ON auth_tokens (created_by) WHERE project_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_users_project ON auth_users (project_id) WHERE project_id IS NOT NULL;
