-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Performance indexes, built without blocking writes (*.no_tx.sql: the migrator
-- sends one statement at a time, each in its own implicit transaction). Runs
-- after 030_perf_indexes.sql, which created pg_trgm.
--
--   * (repository_id, filename) on deb, rpm and pypi: every package download
--     resolves the file name inside one repository.
--   * A partial expression index for the few pypi rows stored with a "--" run
--     in their name (the legacy-name arm of ListPyPIPackageVersions).
--   * GIN trigram indexes over the name expression each branch of the search
--     view filters with ILIKE '%term%'. The expressions must match
--     internal/search/executor.go exactly or the planner cannot use them.
--   * Indexes on the foreign keys that cascades walk on delete.
--   * Drops three indexes that duplicate a unique constraint (write
--     amplification on blobs and proxy_cache_entries for no read benefit).
--
-- Re-runnable: an interrupted CREATE INDEX CONCURRENTLY leaves an INVALID index
-- that IF NOT EXISTS would skip for good, so the first statement drops every
-- invalid index this file creates.

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
            'idx_pypi_packages_repo_filename',
            'idx_deb_packages_repo_filename',
            'idx_rpm_packages_repo_filename',
            'idx_pypi_packages_legacy_name',
            'idx_cargo_crates_name_trgm',
            'idx_conda_packages_name_trgm',
            'idx_conan_packages_name_trgm',
            'idx_docker_tags_name_trgm',
            'idx_gomod_modules_path_trgm',
            'idx_helm_charts_name_trgm',
            'idx_ivy_artifacts_name_trgm',
            'idx_maven_artifacts_name_trgm',
            'idx_npm_packages_name_trgm',
            'idx_nuget_packages_name_trgm',
            'idx_pypi_packages_name_trgm',
            'idx_raw_artifacts_path_trgm',
            'idx_rubygems_gems_name_trgm',
            'idx_sbt_artifacts_name_trgm',
            'idx_rpm_packages_name_trgm',
            'idx_deb_packages_name_trgm',
            'idx_deb_packages_component_id',
            'idx_auth_user_groups_group_id',
            'idx_repository_group_members_member'
          )
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

-- Download by file name ------------------------------------------------------
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_pypi_packages_repo_filename ON pypi_packages (repository_id, filename) WHERE deleted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_deb_packages_repo_filename ON deb_packages (repository_id, filename) WHERE deleted_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rpm_packages_repo_filename ON rpm_packages (repository_id, filename) WHERE deleted_at IS NULL;

-- PyPI legacy names ("--" runs) ----------------------------------------------
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_pypi_packages_legacy_name ON pypi_packages (repository_id, (regexp_replace(name, '-+', '-', 'g'))) WHERE deleted_at IS NULL AND name LIKE '%--%';

-- Search (one GIN trigram index per branch of the search view) ---------------
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_cargo_crates_name_trgm ON cargo_crates USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conda_packages_name_trgm ON conda_packages USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conan_packages_name_trgm ON conan_packages USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_docker_tags_name_trgm ON docker_tags USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_gomod_modules_path_trgm ON gomod_modules USING gin (module_path gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_helm_charts_name_trgm ON helm_charts USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ivy_artifacts_name_trgm ON ivy_artifacts USING gin ((organisation || '/' || module) gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_maven_artifacts_name_trgm ON maven_artifacts USING gin ((group_id || ':' || artifact_id_coord) gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_npm_packages_name_trgm ON npm_packages USING gin (full_name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nuget_packages_name_trgm ON nuget_packages USING gin (package_id gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_pypi_packages_name_trgm ON pypi_packages USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_raw_artifacts_path_trgm ON raw_artifacts USING gin (path gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rubygems_gems_name_trgm ON rubygems_gems USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sbt_artifacts_name_trgm ON sbt_artifacts USING gin ((group_id || '/' || artifact_id) gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rpm_packages_name_trgm ON rpm_packages USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_deb_packages_name_trgm ON deb_packages USING gin (name gin_trgm_ops);

-- Foreign keys walked by cascades ---------------------------------------------
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_deb_packages_component_id ON deb_packages (component_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_user_groups_group_id ON auth_user_groups (group_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_repository_group_members_member ON repository_group_members (member_repo_id);

-- Duplicates of unique constraints --------------------------------------------
DROP INDEX CONCURRENTLY IF EXISTS idx_blobs_sha256;
DROP INDEX CONCURRENTLY IF EXISTS idx_proxy_cache_repository_key;
DROP INDEX CONCURRENTLY IF EXISTS idx_vuln_results_scan_job;
