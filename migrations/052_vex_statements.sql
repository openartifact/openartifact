-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Admin VEX decisions (docs/security/sbom.md): one row per
-- (repository, artifact, vulnerability) saying whether the artifact is really
-- affected. The SBOM generator and the OpenVEX / CycloneDX VEX exports read
-- them next to the scan results; a scan alone never yields "not_affected".
--
-- Only a new table here; its unique index is built with it (it starts empty).

SET LOCAL lock_timeout = '10s';

CREATE TABLE vex_statements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    artifact_id text NOT NULL,
    vuln_id text NOT NULL,
    status character varying(24) NOT NULL,
    justification character varying(64) NOT NULL DEFAULT '',
    impact_statement text NOT NULL DEFAULT '',
    action_statement text NOT NULL DEFAULT '',
    created_by text NOT NULL DEFAULT '',
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT vex_statements_pkey PRIMARY KEY (id),
    CONSTRAINT vex_statements_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
    CONSTRAINT vex_statements_target_key UNIQUE (repository_id, artifact_id, vuln_id),
    CONSTRAINT vex_statements_status_check CHECK (status IN ('affected', 'not_affected', 'fixed', 'under_investigation')),
    CONSTRAINT vex_statements_justification_check CHECK (justification IN (
        '', 'component_not_present', 'vulnerable_code_not_present', 'vulnerable_code_not_in_execute_path',
        'vulnerable_code_cannot_be_controlled_by_adversary', 'inline_mitigations_already_exist')),
    -- "not_affected" is only ever recorded with an explicit justification.
    CONSTRAINT vex_statements_not_affected_justified CHECK (status <> 'not_affected' OR justification <> '')
);

COMMENT ON TABLE vex_statements IS 'Admin VEX decisions per (repository, artifact, vulnerability). justification is the OpenVEX enum.';
