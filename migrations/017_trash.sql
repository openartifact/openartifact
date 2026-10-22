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
--
-- This file only does what is cheap, so it holds its ACCESS EXCLUSIVE locks for
-- a moment even on a large database: new nullable columns (metadata only) and
-- the audit constraint added NOT VALID. The expensive part, swapping every
-- unique constraint for a partial unique index over live rows, builds the
-- indexes CONCURRENTLY and lives in 018_trash_indexes.no_tx.sql, which also
-- validates the audit constraint.

-- A lock that cannot be taken within 10 s aborts (and rolls back) the migration
-- instead of queueing every query on the table behind a long-running one; the
-- next start retries.
SET LOCAL lock_timeout = '10s';

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
-- Columns only. Uniqueness moves to partial unique indexes over live rows (a path
-- can be re-published while an older copy sits in the trash; restoring the older
-- copy then fails with a unique violation, which the API maps to 409), built
-- concurrently by 018_trash_indexes.no_tx.sql. Until that runs, the old
-- constraints still hold for every row, trashed or not.

ALTER TABLE maven_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE ivy_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE sbt_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE npm_versions ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE pypi_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE nuget_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE gomod_modules ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE cargo_crates ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE rubygems_gems ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE conda_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE conan_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE helm_charts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE raw_artifacts ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE docker_manifests ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE docker_tags ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE rpm_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;
ALTER TABLE deb_packages ADD COLUMN deleted_at timestamp with time zone, ADD COLUMN deleted_by text;

-- Audit actions ---------------------------------------------------------------

-- Added NOT VALID: new rows are checked at once, existing rows (all written under
-- the previous, narrower list) are checked by VALIDATE CONSTRAINT in 018, which
-- does not block writes, instead of scanning audit_logs under ACCESS EXCLUSIVE.
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
)) NOT VALID;
