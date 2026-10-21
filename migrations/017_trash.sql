-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Soft delete, trash and deletion protection.
--
-- Deleting a repository, or a package/version/file inside one, no longer removes
-- the rows: it stamps deleted_at/deleted_by and every read path hides the row
-- (WHERE deleted_at IS NULL). The row, and therefore its blob reference, stays
-- until the purge worker removes it once the repository's retention window
-- (trash_retention_days, NULL = the server default) has passed, or an admin
-- purges it. Blob reference counting (storage.SafeDeleteBlob) counts trashed
-- rows, so a blob is only freed by the purge; storage_quotas usage likewise
-- keeps counting trashed bytes until then.

-- Repositories ---------------------------------------------------------------

ALTER TABLE repositories
    ADD COLUMN deleted_at timestamp with time zone,
    ADD COLUMN deleted_by text,
    ADD COLUMN trash_retention_days integer,
    ADD COLUMN immutable_releases boolean DEFAULT false NOT NULL,
    ADD CONSTRAINT chk_repo_trash_retention CHECK (trash_retention_days IS NULL OR trash_retention_days >= 0);

CREATE INDEX idx_repositories_deleted ON repositories (deleted_at) WHERE deleted_at IS NOT NULL;

COMMENT ON COLUMN repositories.deleted_at IS 'Set when the repository was moved to the trash; the name stays reserved (globally unique) until it is purged.';
COMMENT ON COLUMN repositories.deleted_by IS 'Username of the principal that deleted the repository.';
COMMENT ON COLUMN repositories.trash_retention_days IS 'Days a deleted item stays restorable. NULL = server default (OPENARTIFACT_TRASH_RETENTION); 0 = delete immediately, no trash.';
COMMENT ON COLUMN repositories.immutable_releases IS 'When true, released (non-snapshot, non-prerelease) versions cannot be deleted or overwritten without admin force.';

-- Per-format package tables ---------------------------------------------------
-- The unique key of every table becomes a partial unique index over live rows,
-- so a path can be re-published while an older copy sits in the trash. Restoring
-- the older copy then fails with a unique violation, which the API maps to 409.

ALTER TABLE maven_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE maven_artifacts DROP CONSTRAINT maven_artifacts_repository_id_repo_path_key;
CREATE UNIQUE INDEX maven_artifacts_live_repo_path_key ON maven_artifacts (repository_id, repo_path) WHERE deleted_at IS NULL;
CREATE INDEX idx_maven_artifacts_deleted ON maven_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE ivy_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE ivy_artifacts DROP CONSTRAINT ivy_artifacts_repo_id_organisation_module_revision_artifact_key;
CREATE UNIQUE INDEX ivy_artifacts_live_coords_key ON ivy_artifacts (repository_id, organisation, module, revision, artifact_name, artifact_ext) WHERE deleted_at IS NULL;
CREATE INDEX idx_ivy_artifacts_deleted ON ivy_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE sbt_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE sbt_artifacts DROP CONSTRAINT sbt_artifacts_repo_id_group_id_artifact_id_version_filename_key;
CREATE UNIQUE INDEX sbt_artifacts_live_coords_key ON sbt_artifacts (repository_id, group_id, artifact_id, version, filename) WHERE deleted_at IS NULL;
CREATE INDEX idx_sbt_artifacts_deleted ON sbt_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE npm_versions ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE npm_versions DROP CONSTRAINT npm_versions_package_id_version_key;
CREATE UNIQUE INDEX npm_versions_live_pkg_version_key ON npm_versions (package_id, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_npm_versions_deleted ON npm_versions (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE pypi_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE pypi_packages DROP CONSTRAINT pypi_packages_repo_id_name_version_filename_key;
CREATE UNIQUE INDEX pypi_packages_live_coords_key ON pypi_packages (repository_id, name, version, filename) WHERE deleted_at IS NULL;
CREATE INDEX idx_pypi_packages_deleted ON pypi_packages (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE nuget_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE nuget_packages DROP CONSTRAINT nuget_packages_repo_id_package_id_version_key;
CREATE UNIQUE INDEX nuget_packages_live_coords_key ON nuget_packages (repository_id, package_id, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_nuget_packages_deleted ON nuget_packages (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE gomod_modules ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE gomod_modules DROP CONSTRAINT gomod_modules_repo_id_module_path_version_key;
CREATE UNIQUE INDEX gomod_modules_live_coords_key ON gomod_modules (repository_id, module_path, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_gomod_modules_deleted ON gomod_modules (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE cargo_crates ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE cargo_crates DROP CONSTRAINT cargo_crates_repo_id_name_version_key;
CREATE UNIQUE INDEX cargo_crates_live_coords_key ON cargo_crates (repository_id, name, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_cargo_crates_deleted ON cargo_crates (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE rubygems_gems ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE rubygems_gems DROP CONSTRAINT rubygems_gems_repo_id_name_version_platform_key;
CREATE UNIQUE INDEX rubygems_gems_live_coords_key ON rubygems_gems (repository_id, name, version, platform) WHERE deleted_at IS NULL;
CREATE INDEX idx_rubygems_gems_deleted ON rubygems_gems (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE conda_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE conda_packages DROP CONSTRAINT conda_packages_repo_id_name_version_build_string_platform_key;
CREATE UNIQUE INDEX conda_packages_live_coords_key ON conda_packages (repository_id, name, version, build_string, platform) WHERE deleted_at IS NULL;
CREATE INDEX idx_conda_packages_deleted ON conda_packages (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE conan_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE conan_packages DROP CONSTRAINT conan_packages_repo_id_name_version_user_channel_recipe_rev_key;
CREATE UNIQUE INDEX conan_packages_live_coords_key ON conan_packages (repository_id, name, version, user_channel, recipe_revision) WHERE deleted_at IS NULL;
CREATE INDEX idx_conan_packages_deleted ON conan_packages (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE helm_charts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE helm_charts DROP CONSTRAINT helm_charts_repo_id_name_version_key;
CREATE UNIQUE INDEX helm_charts_live_coords_key ON helm_charts (repository_id, name, version) WHERE deleted_at IS NULL;
CREATE INDEX idx_helm_charts_deleted ON helm_charts (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE raw_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE raw_artifacts DROP CONSTRAINT raw_artifacts_repo_id_path_key;
CREATE UNIQUE INDEX raw_artifacts_live_path_key ON raw_artifacts (repository_id, path) WHERE deleted_at IS NULL;
CREATE INDEX idx_raw_artifacts_deleted ON raw_artifacts (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE docker_manifests ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE docker_manifests DROP CONSTRAINT docker_manifests_repository_id_image_digest_key;
CREATE UNIQUE INDEX docker_manifests_live_digest_key ON docker_manifests (repository_id, image, digest) WHERE deleted_at IS NULL;
CREATE INDEX idx_docker_manifests_deleted ON docker_manifests (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE docker_tags ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE docker_tags DROP CONSTRAINT docker_tags_repository_id_image_name_key;
CREATE UNIQUE INDEX docker_tags_live_name_key ON docker_tags (repository_id, image, name) WHERE deleted_at IS NULL;
CREATE INDEX idx_docker_tags_deleted ON docker_tags (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE rpm_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE rpm_packages DROP CONSTRAINT rpm_packages_repository_id_name_version_release_epoch_arch_key;
CREATE UNIQUE INDEX rpm_packages_live_coords_key ON rpm_packages (repository_id, name, version, release, epoch, arch) WHERE deleted_at IS NULL;
CREATE INDEX idx_rpm_packages_deleted ON rpm_packages (deleted_at) WHERE deleted_at IS NOT NULL;

ALTER TABLE deb_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE deb_packages DROP CONSTRAINT deb_packages_repository_id_name_version_arch_component_id_key;
CREATE UNIQUE INDEX deb_packages_live_coords_key ON deb_packages (repository_id, name, version, arch, component_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_deb_packages_deleted ON deb_packages (deleted_at) WHERE deleted_at IS NOT NULL;

-- Audit actions ---------------------------------------------------------------

ALTER TABLE audit_logs DROP CONSTRAINT chk_audit_action;
ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_action CHECK (action IN (
    -- Artifact lifecycle
    'artifact.uploaded', 'artifact.downloaded', 'artifact.deleted', 'artifact.moved',
    'artifact.promoted', 'artifact.quarantined', 'artifact.released',
    'artifact.restored', 'artifact.purged',
    -- Repository management
    'repository.created', 'repository.updated', 'repository.deleted',
    'repository.blocked', 'repository.unblocked',
    'repository.restored', 'repository.purged',
    -- Auth events
    'auth.login', 'auth.logout', 'auth.login_failed', 'auth.token_created',
    'auth.token_revoked', 'auth.password_changed', 'auth.sso_login',
    'auth.token_validate',
    -- Permission management
    'permission.target_created', 'permission.target_updated', 'permission.target_deleted',
    'permission.assigned', 'permission.revoked',
    -- Organization management
    'org.created', 'org.updated', 'org.deleted',
    'org.member_added', 'org.member_removed', 'org.member_role_changed',
    -- System configuration
    'system.setting_changed', 'system.storage_backend_changed',
    -- Security scanning
    'scan.vuln_detected', 'scan.secret_detected', 'scan.quarantine_applied',
    -- Bundle/promotion
    'bundle.created', 'bundle.sealed', 'bundle.archived', 'bundle.promoted',
    -- HTTP audit (middleware logs method:path for generic request auditing)
    'http.request'
));
