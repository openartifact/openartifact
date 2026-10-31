-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Widens chk_audit_action with cleanup.run_completed, the event a cleanup policy
-- run records against its repository when it finishes (with the counts), so the
-- repository's activity shows scheduled runs too. An insert of an action the
-- constraint does not list fails with SQLSTATE 23514 and the event is lost.
-- Added NOT VALID: existing rows are checked by VALIDATE CONSTRAINT in
-- 006_audit_cleanup_run_validate.no_tx.sql. Keep audit.KnownActions and this
-- list in sync (the integration test TestKnownActionsAcceptedByDatabase
-- enforces it).

SET LOCAL lock_timeout = '10s';

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
    -- Organization management (legacy, never emitted)
    'org.created', 'org.updated', 'org.deleted',
    'org.member_added', 'org.member_removed', 'org.member_role_changed',
    -- Project management
    'project.created', 'project.updated', 'project.deleted',
    'project.policy_changed', 'project.quota_changed',
    'project.member_added', 'project.member_removed', 'project.member_role_changed',
    'project.group_added', 'project.group_removed', 'project.group_role_changed',
    'project.token_created', 'project.token_revoked',
    'project.repository_created', 'project.repository_deleted', 'project.repository_transferred',
    'project.cleanup_policy_changed',
    -- System configuration
    'system.setting_changed', 'system.storage_backend_changed',
    -- Security scanning
    'scan.vuln_detected', 'scan.vuln_resolved', 'scan.secret_detected', 'scan.quarantine_applied',
    -- Bundle/promotion
    'bundle.created', 'bundle.sealed', 'bundle.archived', 'bundle.promoted',
    -- HTTP audit
    'http.request',
    -- Admin-plane mutations
    'user.created', 'user.updated', 'user.disabled', 'user.deleted',
    'group.created', 'group.updated', 'group.deleted',
    'group.member_added', 'group.member_removed', 'group.members_set',
    'permission_target.created', 'permission_target.updated', 'permission_target.deleted',
    'permission_target.assigned', 'permission_target.unassigned',
    'sso_provider.created', 'sso_provider.updated', 'sso_provider.deleted',
    'token.created_for_user',
    -- GPG key management
    'gpg_key.created', 'gpg_key.deleted', 'gpg_key.activated', 'gpg_key.deactivated', 'gpg_key.exported',
    -- Backup and restore
    'backup.restore_started',
    -- Repository cleanup policies
    'cleanup.policy_changed', 'cleanup.run_started', 'cleanup.run_completed',
    -- Federation
    'federation.enabled_changed', 'federation.peer_created', 'federation.peer_updated',
    'federation.peer_deleted', 'federation.peer_resynced',
    'federation.token_created', 'federation.token_revoked',
    -- Integrations and policies
    'dependency_track.config_changed', 'license.policy_changed',
    -- Proxy curation rules (005_curation.sql; this list is cumulative, a
    -- rewrite of the constraint must keep every earlier migration's actions)
    'curation.rule_created', 'curation.rule_updated', 'curation.rule_deleted'
)) NOT VALID;
