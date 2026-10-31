-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Service accounts and CI OIDC token exchange.
--
-- A service account is an auth_users row of the new kind service_account: a
-- non-human principal an administrator creates by name. It has no password, no
-- email and no session (the UI login refuses it), belongs to no project (unlike
-- the kind "service" users behind project tokens) and holds permission targets,
-- group memberships, project roles and API tokens exactly like a user.
--
-- oidc_trust_policies are the administrator's rules for exchanging an ID token
-- of an external issuer (GitHub Actions, GitLab CI, ...) for a short-lived
-- token of a service account: the issuer, the audience the CI job asked for,
-- glob conditions on the token's claims and the lifetime of the issued token.
-- The audit action list gains service_account.*, oidc_policy.* and
-- auth.oidc_exchange[_failed] (added NOT VALID; validated in
-- 007_service_accounts_oidc_validate.no_tx.sql; audit.KnownActions lists them too).
SET LOCAL lock_timeout = '10s';

ALTER TABLE public.auth_users DROP CONSTRAINT chk_auth_user_kind;
ALTER TABLE public.auth_users ADD CONSTRAINT chk_auth_user_kind
    CHECK (kind = ANY (ARRAY['human'::text, 'service'::text, 'service_account'::text]));

COMMENT ON COLUMN public.auth_users.kind IS 'human: a person; service: the account behind one project token; service_account: an administrator-managed non-human principal.';

CREATE TABLE public.oidc_trust_policies (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(64) NOT NULL,
    issuer text NOT NULL,
    audience text NOT NULL,
    conditions jsonb DEFAULT '[]'::jsonb NOT NULL,
    service_account_id uuid NOT NULL,
    token_ttl_seconds integer DEFAULT 900 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_by text DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT oidc_trust_policies_pkey PRIMARY KEY (id),
    CONSTRAINT oidc_trust_policies_name_key UNIQUE (name),
    CONSTRAINT oidc_trust_policies_service_account_fkey FOREIGN KEY (service_account_id) REFERENCES public.auth_users(id) ON DELETE CASCADE,
    CONSTRAINT oidc_trust_policies_ttl_check CHECK (token_ttl_seconds BETWEEN 60 AND 3600),
    CONSTRAINT oidc_trust_policies_conditions_check CHECK (jsonb_typeof(conditions) = 'array' AND jsonb_array_length(conditions) > 0)
);

COMMENT ON TABLE public.oidc_trust_policies IS 'Rules exchanging a CI OIDC ID token for a short-lived token of a service account.';

CREATE INDEX idx_oidc_trust_policies_issuer ON public.oidc_trust_policies USING btree (issuer) WHERE enabled;
CREATE INDEX idx_oidc_trust_policies_service_account ON public.oidc_trust_policies USING btree (service_account_id);

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
    'auth.oidc_exchange', 'auth.oidc_exchange_failed'
)) NOT VALID;
