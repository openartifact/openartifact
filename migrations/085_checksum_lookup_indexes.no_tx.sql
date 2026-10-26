-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Indexes for the checksum search (GET /api/v1/search?checksum=): a SHA-1 or
-- MD5 digest is looked up across repositories, so the index must lead with the
-- digest, not with repository_id as the per-repository checksum indexes do.
-- SHA-256 needs none: it resolves through blobs (unique on sha256) and the
-- blob_id indexes of the package tables.
--
-- Built without blocking writes (*.no_tx.sql: one statement at a time).
-- Re-runnable: an interrupted CONCURRENTLY build leaves an INVALID index that
-- IF NOT EXISTS would skip for good, so those are dropped first.
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
          AND c.relname IN (
              'idx_maven_artifacts_sha1_lookup',
              'idx_maven_artifacts_md5_lookup',
              'idx_npm_versions_shasum_lookup',
              'idx_conan_packages_sha1_lookup',
              'idx_conan_packages_md5_lookup',
              'idx_conda_packages_md5_lookup'
          )
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_maven_artifacts_sha1_lookup ON maven_artifacts (sha1) WHERE sha1 <> '' AND deleted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_maven_artifacts_md5_lookup ON maven_artifacts (md5) WHERE md5 <> '' AND deleted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_npm_versions_shasum_lookup ON npm_versions (shasum) WHERE shasum <> '' AND deleted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conan_packages_sha1_lookup ON conan_packages (sha1) WHERE sha1 <> '' AND deleted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conan_packages_md5_lookup ON conan_packages (md5) WHERE md5 <> '' AND deleted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conda_packages_md5_lookup ON conda_packages (md5) WHERE md5 <> '' AND deleted_at IS NULL;
