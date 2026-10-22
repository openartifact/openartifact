-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Soft delete, part 2: live-row unique indexes, built without blocking writes.
--
-- Runs outside a transaction (*.no_tx.sql: the migrator sends one statement at a
-- time, each in its own implicit transaction), after 017_trash.sql added the
-- deleted_at/deleted_by columns. For every per-format package table it
--
--   1. builds the partial unique index over live rows with CREATE UNIQUE INDEX
--      CONCURRENTLY (reads and writes continue; time grows with the table size),
--   2. only then drops the old unique constraint, a catalog change that takes
--      ACCESS EXCLUSIVE for an instant and gives up after 10 s instead of
--      stalling the table behind a long-running query, and
--   3. builds the idx_<table>_deleted index the trash listings and the purge
--      worker use, also CONCURRENTLY.
--
-- Uniqueness is never lost: between 1 and 2 the old constraint and the new index
-- both hold, and ON CONFLICT (cols) WHERE deleted_at IS NULL infers either of
-- them. Finally the audit constraint added NOT VALID by 017 is validated, which
-- takes only SHARE UPDATE EXCLUSIVE.
--
-- Re-runnable: a failed or cancelled CREATE INDEX CONCURRENTLY leaves an INVALID
-- index behind, and IF NOT EXISTS would then skip it for good, so the first
-- statement drops every invalid index this file creates. Everything else is
-- IF NOT EXISTS / IF EXISTS, and a migration that stopped half way simply
-- continues at the next start.
--
-- Rolling upgrade: once the old constraints are gone, a previous version's
-- ON CONFLICT (cols) upsert (without the WHERE deleted_at IS NULL predicate)
-- finds no arbiter and fails. Stop the old replicas before the new version
-- migrates, or accept those errors until they are replaced.

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
            'maven_artifacts_live_repo_path_key',
            'idx_maven_artifacts_deleted',
            'ivy_artifacts_live_coords_key',
            'idx_ivy_artifacts_deleted',
            'sbt_artifacts_live_coords_key',
            'idx_sbt_artifacts_deleted',
            'npm_versions_live_pkg_version_key',
            'idx_npm_versions_deleted',
            'pypi_packages_live_coords_key',
            'idx_pypi_packages_deleted',
            'nuget_packages_live_coords_key',
            'idx_nuget_packages_deleted',
            'gomod_modules_live_coords_key',
            'idx_gomod_modules_deleted',
            'cargo_crates_live_coords_key',
            'idx_cargo_crates_deleted',
            'rubygems_gems_live_coords_key',
            'idx_rubygems_gems_deleted',
            'conda_packages_live_coords_key',
            'idx_conda_packages_deleted',
            'conan_packages_live_coords_key',
            'idx_conan_packages_deleted',
            'helm_charts_live_coords_key',
            'idx_helm_charts_deleted',
            'raw_artifacts_live_path_key',
            'idx_raw_artifacts_deleted',
            'docker_manifests_live_digest_key',
            'idx_docker_manifests_deleted',
            'docker_tags_live_name_key',
            'idx_docker_tags_deleted',
            'rpm_packages_live_coords_key',
            'idx_rpm_packages_deleted',
            'deb_packages_live_coords_key',
            'idx_deb_packages_deleted'
          )
    LOOP
        EXECUTE format('DROP INDEX %s', idx);
    END LOOP;
END
$$;

-- maven_artifacts
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS maven_artifacts_live_repo_path_key ON maven_artifacts (repository_id, repo_path) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE maven_artifacts DROP CONSTRAINT IF EXISTS maven_artifacts_repository_id_repo_path_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_maven_artifacts_deleted ON maven_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

-- ivy_artifacts
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ivy_artifacts_live_coords_key ON ivy_artifacts (repository_id, organisation, module, revision, artifact_name, artifact_ext) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE ivy_artifacts DROP CONSTRAINT IF EXISTS ivy_artifacts_repo_id_organisation_module_revision_artifact_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ivy_artifacts_deleted ON ivy_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

-- sbt_artifacts
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS sbt_artifacts_live_coords_key ON sbt_artifacts (repository_id, group_id, artifact_id, version, filename) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE sbt_artifacts DROP CONSTRAINT IF EXISTS sbt_artifacts_repo_id_group_id_artifact_id_version_filename_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sbt_artifacts_deleted ON sbt_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

-- npm_versions
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS npm_versions_live_pkg_version_key ON npm_versions (package_id, version) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE npm_versions DROP CONSTRAINT IF EXISTS npm_versions_package_id_version_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_npm_versions_deleted ON npm_versions (deleted_at) WHERE deleted_at IS NOT NULL;

-- pypi_packages
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS pypi_packages_live_coords_key ON pypi_packages (repository_id, name, version, filename) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE pypi_packages DROP CONSTRAINT IF EXISTS pypi_packages_repo_id_name_version_filename_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_pypi_packages_deleted ON pypi_packages (deleted_at) WHERE deleted_at IS NOT NULL;

-- nuget_packages
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS nuget_packages_live_coords_key ON nuget_packages (repository_id, package_id, version) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE nuget_packages DROP CONSTRAINT IF EXISTS nuget_packages_repo_id_package_id_version_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nuget_packages_deleted ON nuget_packages (deleted_at) WHERE deleted_at IS NOT NULL;

-- gomod_modules
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS gomod_modules_live_coords_key ON gomod_modules (repository_id, module_path, version) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE gomod_modules DROP CONSTRAINT IF EXISTS gomod_modules_repo_id_module_path_version_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_gomod_modules_deleted ON gomod_modules (deleted_at) WHERE deleted_at IS NOT NULL;

-- cargo_crates
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS cargo_crates_live_coords_key ON cargo_crates (repository_id, name, version) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE cargo_crates DROP CONSTRAINT IF EXISTS cargo_crates_repo_id_name_version_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_cargo_crates_deleted ON cargo_crates (deleted_at) WHERE deleted_at IS NOT NULL;

-- rubygems_gems
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS rubygems_gems_live_coords_key ON rubygems_gems (repository_id, name, version, platform) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE rubygems_gems DROP CONSTRAINT IF EXISTS rubygems_gems_repo_id_name_version_platform_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rubygems_gems_deleted ON rubygems_gems (deleted_at) WHERE deleted_at IS NOT NULL;

-- conda_packages
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS conda_packages_live_coords_key ON conda_packages (repository_id, name, version, build_string, platform) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE conda_packages DROP CONSTRAINT IF EXISTS conda_packages_repo_id_name_version_build_string_platform_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conda_packages_deleted ON conda_packages (deleted_at) WHERE deleted_at IS NOT NULL;

-- conan_packages
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS conan_packages_live_coords_key ON conan_packages (repository_id, name, version, user_channel, recipe_revision) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE conan_packages DROP CONSTRAINT IF EXISTS conan_packages_repo_id_name_version_user_channel_recipe_rev_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conan_packages_deleted ON conan_packages (deleted_at) WHERE deleted_at IS NOT NULL;

-- helm_charts
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS helm_charts_live_coords_key ON helm_charts (repository_id, name, version) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE helm_charts DROP CONSTRAINT IF EXISTS helm_charts_repo_id_name_version_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_helm_charts_deleted ON helm_charts (deleted_at) WHERE deleted_at IS NOT NULL;

-- raw_artifacts
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS raw_artifacts_live_path_key ON raw_artifacts (repository_id, path) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE raw_artifacts DROP CONSTRAINT IF EXISTS raw_artifacts_repo_id_path_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_raw_artifacts_deleted ON raw_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

-- docker_manifests
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS docker_manifests_live_digest_key ON docker_manifests (repository_id, image, digest) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE docker_manifests DROP CONSTRAINT IF EXISTS docker_manifests_repository_id_image_digest_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_docker_manifests_deleted ON docker_manifests (deleted_at) WHERE deleted_at IS NOT NULL;

-- docker_tags
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS docker_tags_live_name_key ON docker_tags (repository_id, image, name) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE docker_tags DROP CONSTRAINT IF EXISTS docker_tags_repository_id_image_name_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_docker_tags_deleted ON docker_tags (deleted_at) WHERE deleted_at IS NOT NULL;

-- rpm_packages
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS rpm_packages_live_coords_key ON rpm_packages (repository_id, name, version, release, epoch, arch) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE rpm_packages DROP CONSTRAINT IF EXISTS rpm_packages_repository_id_name_version_release_epoch_arch_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rpm_packages_deleted ON rpm_packages (deleted_at) WHERE deleted_at IS NOT NULL;

-- deb_packages
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS deb_packages_live_coords_key ON deb_packages (repository_id, name, version, arch, component_id) WHERE deleted_at IS NULL;
DO $$ BEGIN SET LOCAL lock_timeout = '10s'; ALTER TABLE deb_packages DROP CONSTRAINT IF EXISTS deb_packages_repository_id_name_version_arch_component_id_key; END $$;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_deb_packages_deleted ON deb_packages (deleted_at) WHERE deleted_at IS NOT NULL;

-- Audit actions ---------------------------------------------------------------
-- Checks the rows written before 017 against the widened list without blocking
-- inserts. A no-op once validated.
ALTER TABLE audit_logs VALIDATE CONSTRAINT chk_audit_action;
