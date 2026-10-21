-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Package listing (ListRepoComponents) takes, per format table, the newest
-- (page_limit + page_offset) rows of one repository in (created_at DESC, id)
-- order. A (repository_id, created_at DESC, id) index serves that as an ordered
-- scan that stops after the page, instead of reading and sorting every row of
-- the repository. It also covers every lookup by repository_id alone, so the
-- single-column idx_<table>_repository_id indexes it supersedes are dropped.
-- (npm_versions and proxy_cache_entries are not listed: npm filters through
-- npm_packages, and proxy listings are bounded by the cache TTL.)

CREATE INDEX IF NOT EXISTS idx_deb_packages_repo_created
    ON deb_packages (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_deb_packages_repository_id;

CREATE INDEX IF NOT EXISTS idx_rpm_packages_repo_created
    ON rpm_packages (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_rpm_packages_repository_id;

CREATE INDEX IF NOT EXISTS idx_pypi_packages_repo_created
    ON pypi_packages (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_pypi_packages_repository_id;

CREATE INDEX IF NOT EXISTS idx_maven_artifacts_repo_created
    ON maven_artifacts (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_maven_artifacts_repository_id;

CREATE INDEX IF NOT EXISTS idx_ivy_artifacts_repo_created
    ON ivy_artifacts (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_ivy_artifacts_repository_id;

CREATE INDEX IF NOT EXISTS idx_sbt_artifacts_repo_created
    ON sbt_artifacts (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_sbt_artifacts_repository_id;

CREATE INDEX IF NOT EXISTS idx_nuget_packages_repo_created
    ON nuget_packages (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_nuget_packages_repository_id;

CREATE INDEX IF NOT EXISTS idx_gomod_modules_repo_created
    ON gomod_modules (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_gomod_modules_repository_id;

CREATE INDEX IF NOT EXISTS idx_cargo_crates_repo_created
    ON cargo_crates (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_cargo_crates_repository_id;

CREATE INDEX IF NOT EXISTS idx_rubygems_gems_repo_created
    ON rubygems_gems (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_rubygems_gems_repository_id;

CREATE INDEX IF NOT EXISTS idx_conda_packages_repo_created
    ON conda_packages (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_conda_packages_repository_id;

CREATE INDEX IF NOT EXISTS idx_conan_packages_repo_created
    ON conan_packages (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_conan_packages_repository_id;

CREATE INDEX IF NOT EXISTS idx_helm_charts_repo_created
    ON helm_charts (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_helm_charts_repository_id;

CREATE INDEX IF NOT EXISTS idx_raw_artifacts_repo_created
    ON raw_artifacts (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_raw_artifacts_repository_id;

CREATE INDEX IF NOT EXISTS idx_docker_tags_repo_created
    ON docker_tags (repository_id, created_at DESC, id);
DROP INDEX IF EXISTS idx_docker_tags_repository_id;
