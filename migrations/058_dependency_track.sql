-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Dependency-Track integration (docs/security/dependency-track.md).
--
-- repository_dependency_track holds the per-repository override of the global
-- settings (system_settings key dependency_track.global): empty strings and
-- NULL booleans inherit the global value. api_key is encrypted (internal/crypto).
-- dependency_track_pushes records, per artifact, how far the last BOM / VEX
-- push got, so the UI and the API can show it.
--
-- Only new tables here; they start empty, so their indexes are built with them.

SET LOCAL lock_timeout = '10s';

CREATE TABLE repository_dependency_track (
    repository_id uuid NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    base_url text DEFAULT '' NOT NULL,
    api_key text DEFAULT '' NOT NULL,
    project_name text DEFAULT '' NOT NULL,
    project_version text DEFAULT '' NOT NULL,
    auto_push boolean,
    push_vex boolean,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT repository_dependency_track_pkey PRIMARY KEY (repository_id),
    CONSTRAINT repository_dependency_track_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

COMMENT ON TABLE repository_dependency_track IS 'Per-repository Dependency-Track overrides; empty/NULL fields inherit the global settings. api_key is encrypted.';

CREATE TABLE dependency_track_pushes (
    repository_id uuid NOT NULL,
    artifact_id text NOT NULL,
    state character varying(16) NOT NULL,
    detail text DEFAULT '' NOT NULL,
    project_uuid text DEFAULT '' NOT NULL,
    project_name text DEFAULT '' NOT NULL,
    project_version text DEFAULT '' NOT NULL,
    components integer DEFAULT 0 NOT NULL,
    vex_statements integer DEFAULT 0 NOT NULL,
    vex_applied integer DEFAULT 0 NOT NULL,
    bom_pushed_at timestamp with time zone,
    vex_pushed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT dependency_track_pushes_pkey PRIMARY KEY (repository_id, artifact_id),
    CONSTRAINT dependency_track_pushes_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
    CONSTRAINT dependency_track_pushes_state_check CHECK (state IN ('queued', 'running', 'done', 'skipped', 'failed'))
);

COMMENT ON TABLE dependency_track_pushes IS 'Last BOM/VEX push of an artifact to Dependency-Track: state, counts and the DT project.';

-- Per-repository switch for attaching signed SBOM / VEX attestations to docker
-- images as OCI referrers (docs/security/sbom.md). Off by default: a repository
-- without a row does not attach anything.
CREATE TABLE repository_attestation_attach (
    repository_id uuid NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT repository_attestation_attach_pkey PRIMARY KEY (repository_id),
    CONSTRAINT repository_attestation_attach_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);
