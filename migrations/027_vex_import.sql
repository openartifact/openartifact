-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Vendor VEX (CSAF 2.0) feeds and VEX decisions shared between federated
-- instances.
--
-- vex_statements stays the one table the findings read. A statement now says
-- where it came from: source_kind 'local' is an operator decision, 'csaf' a
-- statement materialized from a vendor feed, 'peer' a federated peer's decision
-- an operator accepted (or a peer trusted with 'apply'). Imports never replace
-- a statement of another source; an operator decision replaces anything.
SET LOCAL lock_timeout = '5s';

CREATE TABLE public.vex_feeds (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    require_signature boolean DEFAULT true NOT NULL,
    pgp_keys text DEFAULT ''::text NOT NULL,
    interval_hours integer DEFAULT 24 NOT NULL,
    cursor_at timestamp with time zone,
    last_attempt_at timestamp with time zone,
    last_success_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    documents_total integer DEFAULT 0 NOT NULL,
    rejected_total integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT vex_feeds_pkey PRIMARY KEY (id),
    CONSTRAINT vex_feeds_name_key UNIQUE (name),
    CONSTRAINT vex_feeds_interval_check CHECK (interval_hours BETWEEN 1 AND 720)
);

COMMENT ON TABLE public.vex_feeds IS 'CSAF 2.0 VEX providers (provider-metadata.json, ROLIE feed or single document URL) and their import state';
COMMENT ON COLUMN public.vex_feeds.pgp_keys IS 'Armored OpenPGP public keys pinned for the feed; empty uses the keys listed in the provider metadata';
COMMENT ON COLUMN public.vex_feeds.cursor_at IS 'Newest document release date already imported; the next sync fetches only documents changed after it';

-- The statements of the imported documents, independent of the stored
-- artifacts: matching them against the scanned artifacts (materializing
-- vex_statements rows with source_kind csaf) is redone after every sync and
-- periodically, so an artifact uploaded later is matched too.
CREATE TABLE public.vex_feed_statements (
    id bigint GENERATED ALWAYS AS IDENTITY,
    feed_id uuid NOT NULL,
    document_url text NOT NULL,
    tracking_id text DEFAULT ''::text NOT NULL,
    vuln_id text NOT NULL,
    purl text DEFAULT ''::text NOT NULL,
    cpe text DEFAULT ''::text NOT NULL,
    status character varying(24) NOT NULL,
    justification character varying(64) DEFAULT ''::character varying NOT NULL,
    impact_statement text DEFAULT ''::text NOT NULL,
    action_statement text DEFAULT ''::text NOT NULL,
    released_at timestamp with time zone,
    CONSTRAINT vex_feed_statements_pkey PRIMARY KEY (id),
    CONSTRAINT vex_feed_statements_feed_fkey FOREIGN KEY (feed_id) REFERENCES public.vex_feeds(id) ON DELETE CASCADE,
    CONSTRAINT vex_feed_statements_status_check CHECK (status IN ('not_affected', 'fixed')),
    CONSTRAINT vex_feed_statements_product_check CHECK (purl <> '' OR cpe <> '')
);

CREATE INDEX idx_vex_feed_statements_doc ON public.vex_feed_statements (feed_id, document_url);

COMMENT ON TABLE public.vex_feed_statements IS 'not_affected / fixed statements of imported CSAF VEX documents, one per (vulnerability, product identifier)';
COMMENT ON COLUMN public.vex_feed_statements.purl IS 'Normalized versioned package URL of the product (no qualifiers or subpath); empty when only a CPE identifies it';

ALTER TABLE public.vex_statements
    ADD COLUMN source_kind text DEFAULT 'local' NOT NULL,
    ADD COLUMN source_name text DEFAULT ''::text NOT NULL,
    ADD COLUMN source_ref text DEFAULT ''::text NOT NULL,
    ADD COLUMN feed_id uuid,
    ADD COLUMN peer_id uuid,
    ADD CONSTRAINT vex_statements_source_kind_check CHECK (source_kind IN ('local', 'csaf', 'peer')),
    ADD CONSTRAINT vex_statements_feed_fkey FOREIGN KEY (feed_id) REFERENCES public.vex_feeds(id) ON DELETE CASCADE,
    ADD CONSTRAINT vex_statements_peer_fkey FOREIGN KEY (peer_id) REFERENCES public.federation_peers(id) ON DELETE SET NULL;

-- A CSAF not_affected statement carries a justification flag or an impact
-- statement (CSAF 2.0 6.1.27.9); OpenVEX accepts either as well. Operator
-- decisions keep requiring a justification (checked by the API).
ALTER TABLE public.vex_statements DROP CONSTRAINT vex_statements_not_affected_justified;
ALTER TABLE public.vex_statements ADD CONSTRAINT vex_statements_not_affected_justified
    CHECK (status <> 'not_affected' OR justification <> '' OR impact_statement <> '');

COMMENT ON COLUMN public.vex_statements.source_kind IS 'local (operator decision), csaf (vendor feed) or peer (decision of a federated peer)';
COMMENT ON COLUMN public.vex_statements.source_name IS 'Feed or peer name the statement came from; empty for local';
COMMENT ON COLUMN public.vex_statements.source_ref IS 'CSAF document URL (csaf) or peer base URL (peer); empty for local';
COMMENT ON COLUMN public.vex_statements.feed_id IS 'Feed a csaf statement was materialized from; removing the feed removes its statements';

CREATE INDEX idx_vex_statements_feed ON public.vex_statements (feed_id) WHERE feed_id IS NOT NULL;

-- Federation: a pulling repository chooses per peer what happens to the
-- peer's VEX decisions; a federated repository chooses whether it serves its
-- own decisions to the peers that pull it.
ALTER TABLE public.federation_peers
    ADD COLUMN vex_trust text DEFAULT 'ignore' NOT NULL,
    ADD COLUMN vex_synced_at timestamp with time zone,
    ADD COLUMN vex_error text DEFAULT ''::text NOT NULL,
    ADD CONSTRAINT federation_peers_vex_trust_check CHECK (vex_trust IN ('ignore', 'suggest', 'apply'));

ALTER TABLE public.federation_repos
    ADD COLUMN vex_publish boolean DEFAULT false NOT NULL;

COMMENT ON COLUMN public.federation_peers.vex_trust IS 'ignore: not fetched; suggest: kept as pending suggestions; apply: applied where no other statement exists, else pending';
COMMENT ON COLUMN public.federation_repos.vex_publish IS 'Serve this repository''s local VEX decisions (OpenVEX) to the peers that pull it';

CREATE TABLE public.vex_suggestions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    peer_id uuid NOT NULL,
    artifact_id text NOT NULL,
    vuln_id text NOT NULL,
    status character varying(24) NOT NULL,
    justification character varying(64) DEFAULT ''::character varying NOT NULL,
    impact_statement text DEFAULT ''::text NOT NULL,
    action_statement text DEFAULT ''::text NOT NULL,
    peer_updated_at timestamp with time zone,
    state text DEFAULT 'pending' NOT NULL,
    decided_by text DEFAULT ''::text NOT NULL,
    decided_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT vex_suggestions_pkey PRIMARY KEY (id),
    CONSTRAINT vex_suggestions_target_key UNIQUE (peer_id, artifact_id, vuln_id),
    CONSTRAINT vex_suggestions_repository_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE,
    CONSTRAINT vex_suggestions_peer_fkey FOREIGN KEY (peer_id) REFERENCES public.federation_peers(id) ON DELETE CASCADE,
    CONSTRAINT vex_suggestions_state_check CHECK (state IN ('pending', 'accepted', 'dismissed')),
    CONSTRAINT vex_suggestions_status_check CHECK (status IN ('affected', 'not_affected', 'fixed', 'under_investigation')),
    CONSTRAINT vex_suggestions_justification_check CHECK (justification IN ('', 'component_not_present', 'vulnerable_code_not_present',
        'vulnerable_code_not_in_execute_path', 'vulnerable_code_cannot_be_controlled_by_adversary', 'inline_mitigations_already_exist')),
    CONSTRAINT vex_suggestions_not_affected_justified CHECK (status <> 'not_affected' OR justification <> '' OR impact_statement <> '')
);

CREATE INDEX idx_vex_suggestions_repo_state ON public.vex_suggestions (repository_id, state);

COMMENT ON TABLE public.vex_suggestions IS 'VEX decisions of a federated peer, advisory until an operator accepts or dismisses them';
COMMENT ON COLUMN public.vex_suggestions.state IS 'pending, accepted or dismissed; a peer changing its decision moves the suggestion back to pending';

-- Audit actions of the VEX feed administration and of the review of peer
-- suggestions (audit.KnownActions). This list is cumulative: it keeps every
-- earlier migration's actions. Validated in 027_vex_import_validate.no_tx.sql.
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
    'bundle.check_reported',
    -- CSAF VEX feeds and federated VEX suggestions (027_vex_import.sql)
    'vex.feed_created', 'vex.feed_updated', 'vex.feed_deleted', 'vex.feed_synced',
    'vex.suggestion_accepted', 'vex.suggestion_dismissed'
)) NOT VALID;
