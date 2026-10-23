-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Index of 057_conan_checksums.sql, built without blocking writes (*.no_tx.sql:
-- one statement at a time). Re-runnable: an interrupted CONCURRENTLY build
-- leaves an INVALID index that IF NOT EXISTS would skip for good, so it is
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
          AND c.relname = 'idx_conan_packages_sha1'
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

-- Checksum deploy: find a live file of the repository by its SHA-1.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conan_packages_sha1 ON conan_packages (repository_id, sha1) WHERE sha1 <> '' AND deleted_at IS NULL;
