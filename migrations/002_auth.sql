-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Authentication / authorization fixes.

-- permission_targets.org_id is NOT NULL with no default, and every permission-target
-- INSERT (API + sqlc) omits it, so creating a target failed. Repositories are all
-- created in the system organization, so default targets to the same organization.
ALTER TABLE permission_targets
    ALTER COLUMN org_id SET DEFAULT '00000000-0000-0000-0000-000000000001';

-- Replay cache for accepted SAML assertions: an assertion ID may be consumed once
-- until its NotOnOrAfter, across every server replica.
CREATE TABLE auth_saml_assertions (
    assertion_id text NOT NULL,
    provider_slug text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT auth_saml_assertions_pkey PRIMARY KEY (provider_slug, assertion_id)
);

CREATE INDEX idx_auth_saml_assertions_expires_at ON auth_saml_assertions (expires_at);

COMMENT ON TABLE auth_saml_assertions IS 'Consumed SAML assertion IDs (replay protection); rows are purgeable after expires_at';
