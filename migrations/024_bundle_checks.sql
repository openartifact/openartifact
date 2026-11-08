-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Required external checks of a release bundle.
--
-- A CI system (a Tekton pipeline running Conforma, a scanner, a manual review)
-- reports the result of a named check against a bundle; a promotion pipeline
-- stage may list required_checks (inside its stages JSON, no column change) and
-- a promotion into that stage is refused until every one is a live success.
-- One row per (bundle, check name): a new report replaces the previous one.
-- The table joins the recovery journal: a rebuild that lost the rows would turn
-- every gated promotion into a refusal until the pipelines report again.
-- The audit action list gains bundle.check_reported (added NOT VALID; validated
-- in 024_bundle_checks_validate.no_tx.sql; audit.KnownActions lists it too).
SET LOCAL lock_timeout = '10s';

CREATE TABLE public.bundle_checks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    bundle_id uuid NOT NULL,
    name character varying(64) NOT NULL,
    status character varying(16) NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    details_url text DEFAULT ''::text NOT NULL,
    reported_by text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT bundle_checks_pkey PRIMARY KEY (id),
    CONSTRAINT bundle_checks_bundle_name_key UNIQUE (bundle_id, name),
    CONSTRAINT bundle_checks_bundle_fkey FOREIGN KEY (bundle_id) REFERENCES public.release_bundles(id) ON DELETE CASCADE,
    CONSTRAINT bundle_checks_status_check CHECK (status::text = ANY (ARRAY['pending'::text, 'success'::text, 'failure'::text, 'error'::text]))
);

COMMENT ON TABLE public.bundle_checks IS 'Latest result of each named external check reported against a release bundle; gates promotion into stages that require it.';

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.bundle_checks
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
    'cleanup.policy_changed', 'cleanup.run_started', 'cleanup.run_completed',
    -- Federation
    'federation.enabled_changed', 'federation.peer_created', 'federation.peer_updated',
    'federation.peer_deleted', 'federation.peer_resynced',
    'federation.token_created', 'federation.token_revoked',
    -- Integrations and policies
    'dependency_track.config_changed', 'license.policy_changed',
    -- Proxy curation rules (005_curation.sql; this list is cumulative, a
    -- rewrite of the constraint must keep every earlier migration's actions)
    'curation.rule_created', 'curation.rule_updated', 'curation.rule_deleted',
    -- Service accounts and CI OIDC trust policies (007_service_accounts_oidc.sql)
    'service_account.created', 'service_account.updated', 'service_account.deleted',
    'oidc_policy.created', 'oidc_policy.updated', 'oidc_policy.deleted',
    'auth.oidc_exchange', 'auth.oidc_exchange_failed',
    -- Required external checks of a release bundle (024_bundle_checks.sql)
    'bundle.check_reported'
)) NOT VALID;
