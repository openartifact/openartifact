-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Serves the blob reference checks (SafeDeleteBlob, orphan collection), which
-- now also look at generated-metadata rows by content digest (the matching
-- package_deltas index is 028_package_deltas_blob_index.sql). Built
-- without blocking writes (*.no_tx.sql: one statement at a time). Re-runnable:
-- an interrupted CONCURRENTLY build leaves an INVALID index that IF NOT EXISTS
-- would skip for good, so those are dropped first.
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
          AND c.relname = 'idx_repository_metadata_blob_sha256'
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_repository_metadata_blob_sha256 ON repository_metadata (blob_sha256);
