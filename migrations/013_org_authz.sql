-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Organizations take part in authorization. Members of an organization get
-- access to every repository the organization owns: owners and admins get
-- admin (read, write, delete); plain members get the organization's
-- member_role. The built-in system organization
-- (00000000-0000-0000-0000-000000000001) is exempt in code: it is the legacy
-- default organization and its membership grants nothing, so existing
-- deployments behave exactly as before.
ALTER TABLE organizations
    ADD COLUMN member_role text NOT NULL DEFAULT 'read',
    ADD CONSTRAINT chk_org_member_role_level CHECK (member_role IN ('read', 'write', 'admin'));

COMMENT ON COLUMN organizations.member_role IS 'Access level (read, write, admin) every plain member of the organization gets on the organization''s repositories. Owners and admins always get admin. Ignored for the built-in system organization.';

-- Repositories are addressed by name alone (/repos/{name}, /v2/{name}/...), so
-- a name must be unique across organizations, not just within one. Every
-- existing repository lives in the system organization, where (org_id, name)
-- already guarantees this.
CREATE UNIQUE INDEX idx_repositories_name_unique ON repositories (name);

-- backup_jobs: the CHECK constraints from 001 allow values the code never
-- writes and refuse ones it does. The API accepts the types 'full' and
-- 'metadata-only' (an 'incremental' backup was never implemented: the handler
-- rejects it and the engine has no such mode), and the engine finishes a job as
-- 'completed', not 'success'. As it stood, a metadata-only backup could not even
-- be created and no backup could be marked finished. Rows holding the old
-- values cannot have been produced by the application; they are mapped to the
-- closest value that exists so the stricter constraints can be added.
UPDATE backup_jobs SET type = 'full' WHERE type = 'incremental';
UPDATE backup_jobs SET status = 'completed' WHERE status = 'success';

ALTER TABLE backup_jobs
    DROP CONSTRAINT chk_backup_type,
    DROP CONSTRAINT chk_backup_status,
    ADD CONSTRAINT chk_backup_type CHECK (type IN ('full', 'metadata-only')),
    ADD CONSTRAINT chk_backup_status CHECK (status IN ('pending', 'running', 'completed', 'failed'));
