-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Database-managed GPG signing keys (docs/security/signing.md).
--
-- gpg_keys holds the keys the admin imported or generated. The private key is
-- stored as an armored, passphrase-less OpenPGP block sealed with
-- internal/crypto (AES-256-GCM, OPENARTIFACT_SECRET_KEY); it is never returned
-- by the API. At most one key is the global active key; gpg_key_repositories
-- overrides it per repository (deb and rpm metadata signing).
--
-- Also widens chk_audit_action with the gpg_key.* actions (NOT VALID here, the
-- existing rows are checked in 054_gpg_keys_validate.no_tx.sql). Keep
-- audit.KnownActions and the list below in sync (integration test enforces it).
--
-- Only new tables plus a constraint swap: no large-table rewrite.

SET LOCAL lock_timeout = '10s';

CREATE TABLE gpg_keys (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(255) NOT NULL,
    fingerprint character varying(64) NOT NULL,
    key_id character varying(16) NOT NULL,
    algorithm character varying(32) NOT NULL,
    user_id text NOT NULL DEFAULT '',
    public_key text NOT NULL,
    private_key_enc text NOT NULL,
    is_global_active boolean NOT NULL DEFAULT false,
    source character varying(16) NOT NULL DEFAULT 'imported',
    created_by text NOT NULL DEFAULT '',
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    CONSTRAINT gpg_keys_pkey PRIMARY KEY (id),
    CONSTRAINT gpg_keys_name_key UNIQUE (name),
    CONSTRAINT gpg_keys_fingerprint_key UNIQUE (fingerprint),
    CONSTRAINT gpg_keys_source_check CHECK (source IN ('imported', 'generated', 'auto'))
);

COMMENT ON TABLE gpg_keys IS 'GPG signing keys managed through the admin API. private_key_enc is sealed by internal/crypto and never returned.';

-- At most one global active key.
CREATE UNIQUE INDEX gpg_keys_one_global_active ON gpg_keys (is_global_active) WHERE is_global_active;

CREATE TABLE gpg_key_repositories (
    repository_id uuid NOT NULL,
    gpg_key_id uuid NOT NULL,
    assigned_by text NOT NULL DEFAULT '',
    assigned_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT gpg_key_repositories_pkey PRIMARY KEY (repository_id),
    CONSTRAINT gpg_key_repositories_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
    CONSTRAINT gpg_key_repositories_gpg_key_id_fkey FOREIGN KEY (gpg_key_id) REFERENCES gpg_keys(id) ON DELETE CASCADE
);

CREATE INDEX gpg_key_repositories_key_idx ON gpg_key_repositories (gpg_key_id);

COMMENT ON TABLE gpg_key_repositories IS 'Per-repository signing key override; wins over the global active key.';

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
    'scan.vuln_detected', 'scan.secret_detected', 'scan.quarantine_applied',
    -- Bundle/promotion
    'bundle.created', 'bundle.sealed', 'bundle.archived', 'bundle.promoted',
    -- HTTP audit (middleware logs method:path for generic request auditing)
    'http.request',
    -- Admin-plane mutations (auditAdmin)
    'user.created', 'user.updated', 'user.disabled',
    'group.created', 'group.updated', 'group.deleted',
    'group.member_added', 'group.member_removed', 'group.members_set',
    'permission_target.created', 'permission_target.updated', 'permission_target.deleted',
    'permission_target.assigned', 'permission_target.unassigned',
    'sso_provider.created', 'sso_provider.updated', 'sso_provider.deleted',
    'token.created_for_user',
    -- GPG key management
    'gpg_key.created', 'gpg_key.deleted', 'gpg_key.activated', 'gpg_key.deactivated', 'gpg_key.exported'
)) NOT VALID;

-- Per-repository opt-in: sign uploaded RPMs with the active GPG key.
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS sign_packages boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN repositories.sign_packages IS 'Hosted rpm repositories only: sign uploaded RPMs with the active GPG key.';
