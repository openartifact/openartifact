-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Project quotas and project-level cleanup policies.
--
-- project_quotas is a ceiling over the sum of a project's repositories (the
-- repository limits in storage_quotas keep applying: an upload must fit both).
-- Its current_* counters are only maintained while a limit is set; enabling a
-- limit initialises them from the repository counters.
--
-- A cleanup policy belongs either to one repository or to a project (default
-- for the project's hosted repositories without an enabled policy of their own).
SET LOCAL lock_timeout = '10s';

CREATE TABLE project_quotas (
    project_id uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    max_size_bytes bigint NOT NULL DEFAULT 0 CHECK (max_size_bytes >= 0),
    max_artifact_count bigint NOT NULL DEFAULT 0 CHECK (max_artifact_count >= 0),
    warn_threshold_percent integer NOT NULL DEFAULT 80 CHECK (warn_threshold_percent BETWEEN 0 AND 100),
    current_size_bytes bigint NOT NULL DEFAULT 0,
    current_artifact_count bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER update_project_quotas_updated_at
    BEFORE UPDATE ON project_quotas
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE project_quotas IS 'Project storage ceiling (0 = unlimited); counters are maintained only while a limit is set';

ALTER TABLE cleanup_policies ALTER COLUMN repository_id DROP NOT NULL;
ALTER TABLE cleanup_policies
    ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
    ADD CONSTRAINT chk_cleanup_policy_scope CHECK ((repository_id IS NULL) <> (project_id IS NULL)) NOT VALID;

COMMENT ON COLUMN cleanup_policies.project_id IS 'Set for a project default policy (repository_id is then NULL).';
