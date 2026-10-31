-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Package curation for proxy repositories: repositories.curation_mode (off,
-- denylist, allowlist) and curation_rules, the block/allow patterns of one
-- repository. The rules table is journaled (recovery_journal) so a rebuilt
-- instance does not silently serve what an administrator had blocked. The
-- audit action list gains curation.rule_* (added NOT VALID; validated in
-- 005_curation_validate.no_tx.sql; audit.KnownActions lists them too).
SET LOCAL lock_timeout = '10s';

ALTER TABLE public.repositories
    ADD COLUMN curation_mode text DEFAULT 'off' NOT NULL,
    ADD CONSTRAINT repositories_curation_mode_check CHECK (curation_mode IN ('off', 'denylist', 'allowlist'));

CREATE TABLE public.curation_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    action text NOT NULL,
    package_pattern text NOT NULL,
    version_pattern text DEFAULT '' NOT NULL,
    reason text DEFAULT '' NOT NULL,
    created_by text DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT curation_rules_pkey PRIMARY KEY (id),
    CONSTRAINT curation_rules_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE,
    CONSTRAINT curation_rules_action_check CHECK (action IN ('block', 'allow')),
    CONSTRAINT curation_rules_package_pattern_check CHECK (length(package_pattern) BETWEEN 1 AND 255),
    CONSTRAINT curation_rules_version_pattern_check CHECK (length(version_pattern) <= 128),
    CONSTRAINT curation_rules_reason_check CHECK (length(reason) <= 500)
);

CREATE INDEX idx_curation_rules_repository ON public.curation_rules USING btree (repository_id, created_at, id);

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.curation_rules
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();

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
    'cleanup.policy_changed', 'cleanup.run_started',
    -- Federation
    'federation.enabled_changed', 'federation.peer_created', 'federation.peer_updated',
    'federation.peer_deleted', 'federation.peer_resynced',
    'federation.token_created', 'federation.token_revoked',
    -- Integrations and policies
    'dependency_track.config_changed', 'license.policy_changed',
    -- Proxy curation rules
    'curation.rule_created', 'curation.rule_updated', 'curation.rule_deleted'
)) NOT VALID;
